#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
#  k6 + Grafana + InfluxDB 부하 테스트 런처 (Ramp-up 패턴)
#
#  사용법:
#    ./run.sh                                    # 기본값으로 실행
#    RATE=100 ./run.sh                           # 피크 100 req/s
#    RATE=100 RAMPUP=3m SUSTAIN=5m ./run.sh      # 상세 설정
#    ./run.sh stop                               # 스택 종료
# ─────────────────────────────────────────────────────────────

set -euo pipefail

URL="${URL:-http://localhost:8080/v1/stress}"
RATE="${RATE:-56}"         # 피크 req/s  (56 ≈ 200,000 req/h)
RAMPUP="${RAMPUP:-2m}"     # 0 → RATE 증가 구간
SUSTAIN="${SUSTAIN:-3m}"   # RATE 유지 구간 (피크)
RAMPDOWN="${RAMPDOWN:-1m}" # RATE → 0 감소 구간

INFLUX_OUT="influxdb=http://localhost:8086/k6"

# ── 퍼블릭 IP 자동 감지 (EC2 메타데이터 → 로컬 IP → localhost) ──
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

# ── 종료 커맨드 ──────────────────────────────────────────────
if [[ "${1:-}" == "stop" ]]; then
  echo "🛑  모니터링 스택 종료..."
  docker compose down
  echo "✅  완료"
  exit 0
fi

echo ""
echo "╔═══════════════════════════════════════════════════════╗"
echo "║     k6 Load Test + Grafana  (Ramp-up 패턴)           ║"
echo "╚═══════════════════════════════════════════════════════╝"
echo ""
echo "  URL      : ${URL}"
echo "  Peak RPS  : ${RATE} req/s  (~$((RATE * 3600)) req/h)"
echo ""
echo "  ── 트래픽 패턴 ──────────────────────────────────────"
echo "  [${RAMPUP}]    0 → ${RATE} req/s  (점진 증가)"
echo "  [${SUSTAIN}]   ${RATE} req/s 유지  (피크)"
echo "  [${RAMPDOWN}]  ${RATE} → 0 req/s  (점진 감소)"
echo ""

# ── 1. Docker 스택 시작 ──────────────────────────────────────
echo "🐳  모니터링 스택 시작 (InfluxDB + Grafana)..."
docker compose up -d

# ── 2. 서비스 준비 대기 ──────────────────────────────────────
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
echo "┌─────────────────────────────────────────────────┐"
echo "│  📊 Grafana 대시보드                             │"
echo "│  ${GRAFANA_URL}"
echo "│  (브라우저에서 위 주소로 접속)                    │"
echo "└─────────────────────────────────────────────────┘"
echo ""

# ── 3. k6 실행 ───────────────────────────────────────────────
echo "🚀  k6 부하 테스트 시작..."
echo ""
k6 run \
  --out "${INFLUX_OUT}" \
  -e URL="${URL}" \
  -e RATE="${RATE}" \
  -e RAMPUP="${RAMPUP}" \
  -e SUSTAIN="${SUSTAIN}" \
  -e RAMPDOWN="${RAMPDOWN}" \
  k6/script.js

echo ""
echo "✅  테스트 완료. Grafana에서 결과를 확인하세요:"
echo "    ${GRAFANA_URL}"
echo ""
echo "  모니터링 스택을 종료하려면: ./run.sh stop"
