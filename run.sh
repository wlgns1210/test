#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
#  k6 + Grafana + InfluxDB 부하 테스트 런처
#
#  API 설정 방법:
#    apis.json 파일을 편집하여 테스트할 API 목록을 설정합니다.
#    (각 API별 URL / Method / Body / Params / 기대 응답코드 설정 가능)
#
#  PATTERN 선택:
#    rampup (기본) - 점진 증가 → 유지 → 감소
#    step          - 계단식 증가
#    spike         - 기준 트래픽 → 급등 → 복귀
#
#  사용법:
#    ./run.sh                              # rampup 기본값
#    PATTERN=step RATE=100 ./run.sh        # step 패턴
#    PATTERN=spike RATE=200 ./run.sh       # spike 패턴
#    ABNORMAL_RATE=20 ./run.sh             # 20% 비정상 요청 혼합
#    ./run.sh stop                         # 스택 종료
# ─────────────────────────────────────────────────────────────

set -euo pipefail

# ── 공통 변수 ─────────────────────────────────────────────────
RATE="${RATE:-56}"
PATTERN="${PATTERN:-rampup}"
ABNORMAL_RATE="${ABNORMAL_RATE:-0}"

# ── rampup 전용 변수 ──────────────────────────────────────────
RAMPUP="${RAMPUP:-2m}"
SUSTAIN="${SUSTAIN:-3m}"
RAMPDOWN="${RAMPDOWN:-1m}"

# ── step 전용 변수 ────────────────────────────────────────────
STEPS="${STEPS:-4}"
STEP_DURATION="${STEP_DURATION:-1m}"

# ── spike 전용 변수 ───────────────────────────────────────────
BASELINE="${BASELINE:-5}"
WARMUP="${WARMUP:-30s}"
SPIKE_DURATION="${SPIKE_DURATION:-3m}"
COOLDOWN="${COOLDOWN:-30s}"

INFLUX_OUT="influxdb=http://localhost:8086/k6"

# ── 퍼블릭 IP 감지 ────────────────────────────────────────────
detect_ip() {
  TOKEN=$(curl -sf --max-time 2 \
    -X PUT "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null || echo "")
  if [[ -n "$TOKEN" ]]; then
    IP=$(curl -sf --max-time 2 \
      -H "X-aws-ec2-metadata-token: $TOKEN" \
      http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || echo "")
    [[ -n "$IP" ]] && echo "$IP" && return
  fi
  IP=$(curl -sf --max-time 2 \
    http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || echo "")
  [[ -n "$IP" ]] && echo "$IP" && return
  IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "")
  [[ -n "$IP" ]] && echo "$IP" && return
  echo "localhost"
}

HOST_IP=$(detect_ip)
GRAFANA_URL="http://${HOST_IP}"

# ── 종료 커맨드 ───────────────────────────────────────────────
if [[ "${1:-}" == "stop" ]]; then
  echo "🛑  모니터링 스택 종료..."
  docker compose down
  echo "✅  완료"
  exit 0
fi

# ── apis.json 존재 확인 ───────────────────────────────────────
if [[ ! -f "apis.json" ]]; then
  echo "❌  apis.json 파일이 없습니다."
  echo ""
  echo "    다음 형식으로 apis.json을 생성하세요:"
  echo ""
  cat << 'EXAMPLE'
[
  {
    "name": "상품 등록",
    "url": "http://localhost:8080/v1/product",
    "method": "POST",
    "headers": { "Content-Type": "application/json" },
    "body": {
      "requestid": "{{requestid}}",
      "uuid": "{{uuid}}",
      "id": "item{{random6}}",
      "price": "{{randomInt}}"
    },
    "expectedStatus": 201
  },
  {
    "name": "상품 조회",
    "url": "http://localhost:8080/v1/product",
    "method": "GET",
    "params": {
      "id": "item{{random6}}",
      "requestid": "{{requestid}}",
      "uuid": "{{uuid}}"
    },
    "expectedStatus": 200
  }
]
EXAMPLE
  echo ""
  echo "    템플릿 변수:"
  echo "      {{requestid}} → 12자리 랜덤 숫자"
  echo "      {{uuid}}      → UUID v4"
  echo "      {{random6}}   → 6자리 랜덤 숫자"
  echo "      {{randomInt}} → 랜덤 정수 (단독 사용 시 number 타입)"
  exit 1
fi

# ── apis.json 파싱 (Python3 이용) ─────────────────────────────
API_INFO=$(python3 << 'PYEOF'
import json, sys

try:
    with open('apis.json') as f:
        apis = json.load(f)
except Exception as e:
    print(f"ERROR: apis.json 파싱 실패: {e}", file=sys.stderr)
    sys.exit(1)

if not apis:
    print("ERROR: apis.json이 비어 있습니다.", file=sys.stderr)
    sys.exit(1)

print(len(apis))
for i, api in enumerate(apis, 1):
    method = api.get('method', 'POST').upper()
    url    = api.get('url', '')
    name   = api.get('name', '')
    status = api.get('expectedStatus', 200)
    suffix = f'  ({name})' if name else ''
    print(f"    {i}. [{method}] {url} → {status}{suffix}")
PYEOF
)

if [[ $? -ne 0 ]]; then
  echo "$API_INFO"
  exit 1
fi

# 첫 줄 = API 개수, 나머지 = 출력용 텍스트
URL_COUNT=$(echo "$API_INFO" | head -1)
API_LIST=$(echo "$API_INFO" | tail -n +2)

# ── 패턴별 정보 출력 함수 ─────────────────────────────────────
print_pattern_info() {
  case "${PATTERN}" in
    rampup)
      echo "  패턴    : RAMPUP (점진 증가)"
      echo ""
      echo "  req/s"
      echo "   ${RATE} |        ┌──────────┐"
      echo "      |       /            \\"
      echo "    0 |──────/              \\────"
      echo "      | ${RAMPUP} 증가  ${SUSTAIN} 유지  ${RAMPDOWN} 감소"
      echo ""
      echo "  변수 설정:"
      echo "    RATE=${RATE}  RAMPUP=${RAMPUP}  SUSTAIN=${SUSTAIN}  RAMPDOWN=${RAMPDOWN}"
      ;;
    step)
      echo "  패턴    : STEP (계단식 증가)"
      echo ""
      echo "  req/s"
      echo "   ${RATE} |                    ┌──────────────┐"
      echo "      |               ┌───┘              │"
      echo "      |          ┌───┘                   │"
      echo "      |     ┌───┘                        │"
      echo "    0 |─────┘                            └──"
      echo "      | 즉시점프+${STEP_DURATION} 유지 (×${STEPS}단계)   ${SUSTAIN} 피크"
      echo ""
      echo "  변수 설정:"
      echo "    RATE=${RATE}  STEPS=${STEPS}  STEP_DURATION=${STEP_DURATION}  SUSTAIN=${SUSTAIN}"
      ;;
    spike)
      echo "  패턴    : SPIKE (급등)"
      echo ""
      echo "  req/s"
      echo "   ${RATE} |        ┌──────────────┐"
      echo "      |       /│              │\\"
      echo "  ${BASELINE} |──────/ │              │ \\──────"
      echo "      | ${WARMUP}  5s   ${SPIKE_DURATION} 유지   5s  ${COOLDOWN}"
      echo "      | 준비  급등               급감  복귀"
      echo ""
      echo "  변수 설정:"
      echo "    RATE=${RATE}  BASELINE=${BASELINE}  WARMUP=${WARMUP}  SPIKE_DURATION=${SPIKE_DURATION}  COOLDOWN=${COOLDOWN}"
      ;;
    *)
      echo "  ❌ 알 수 없는 패턴: ${PATTERN}"
      echo "     사용 가능: rampup | step | spike"
      exit 1
      ;;
  esac
}

echo ""
echo "╔════════════════════════════════════════════════════════╗"
echo "║          k6 Load Test + Grafana Monitoring             ║"
echo "╚════════════════════════════════════════════════════════╝"
echo ""
echo "  Peak RPS      : ${RATE} req/s × ${URL_COUNT}개 API  (~$((RATE * URL_COUNT * 3600)) req/h)"
echo ""
echo "  대상 API (각각 ${RATE} req/s 독립 부하):"
echo "${API_LIST}"

if [[ "${ABNORMAL_RATE}" -gt 0 ]]; then
  echo ""
  echo "  ⚠️  비정상 요청 : ${ABNORMAL_RATE}%  (정상 $((100 - ABNORMAL_RATE))% + 비정상 ${ABNORMAL_RATE}%)"
  echo "     유형: 빈바디 / 필드누락 / SQL인젝션 / XSS / 초대용량 / 잘못된Content-Type / 존재하지않는경로"
fi
echo ""
print_pattern_info
echo ""

# ── 1. Docker 스택 시작 ───────────────────────────────────────
echo "🐳  모니터링 스택 시작 (InfluxDB + Grafana)..."
docker compose up -d

# ── 2. 서비스 준비 대기 ───────────────────────────────────────
echo "⏳  서비스 준비 중..."
for i in {1..30}; do
  if curl -sf http://localhost:8086/ping > /dev/null 2>&1 && \
     curl -sf "http://localhost/api/health" > /dev/null 2>&1; then
    break
  fi
  echo -n "."
  sleep 2
done
echo ""
echo "✅  서비스 준비 완료"
echo ""
echo "┌──────────────────────────────────────────────────────┐"
echo "│  📊 Grafana 대시보드                                  │"
echo "│  ${GRAFANA_URL}"
echo "│  (브라우저에서 위 주소로 접속)                         │"
echo "└──────────────────────────────────────────────────────┘"
echo ""

# ── 3. k6 실행 ────────────────────────────────────────────────
echo "🚀  k6 부하 테스트 시작  [${PATTERN^^}]  (${URL_COUNT}개 API × ${RATE} req/s 독립 부하)..."
echo ""
k6 run \
  --out "${INFLUX_OUT}" \
  -e RATE="${RATE}" \
  -e PATTERN="${PATTERN}" \
  -e ABNORMAL_RATE="${ABNORMAL_RATE}" \
  -e RAMPUP="${RAMPUP}" \
  -e SUSTAIN="${SUSTAIN}" \
  -e RAMPDOWN="${RAMPDOWN}" \
  -e STEPS="${STEPS}" \
  -e STEP_DURATION="${STEP_DURATION}" \
  -e BASELINE="${BASELINE}" \
  -e WARMUP="${WARMUP}" \
  -e SPIKE_DURATION="${SPIKE_DURATION}" \
  -e COOLDOWN="${COOLDOWN}" \
  k6/script.js

echo ""
echo "✅  테스트 완료. Grafana에서 결과를 확인하세요:"
echo "    ${GRAFANA_URL}"
echo ""
echo "  모니터링 스택을 종료하려면: ./run.sh stop"
