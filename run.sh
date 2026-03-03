#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────
#  k6 + Grafana + InfluxDB 부하 테스트 런처
#
#  사용법:
#    ./run.sh                          # 기본값으로 실행
#    URL=http://host/api ./run.sh      # URL 지정
#    RATE=100 DURATION=5m ./run.sh     # 100 req/s, 5분
#    ./run.sh stop                     # 스택 종료
# ─────────────────────────────────────────────────────────────

set -euo pipefail

URL="${URL:-http://localhost:8080/v1/stress}"
RATE="${RATE:-56}"          # req/s  (56 ≈ 200,000 req/h)
DURATION="${DURATION:-1m}"

INFLUX_OUT="influxdb=http://localhost:8086/k6"

# ── 퍼블릭 IP 자동 감지 (EC2 메타데이터 → 실패 시 로컬 IP → localhost) ──
detect_ip() {
  # EC2 IMDSv2 방식으로 퍼블릭 IP 조회
  TOKEN=$(curl -sf --max-time 2 \
    -X PUT "http://169.254.169.254/latest/api/token" \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null || echo "")

  if [[ -n "$TOKEN" ]]; then
    IP=$(curl -sf --max-time 2 \
      -H "X-aws-ec2-metadata-token: $TOKEN" \
      http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || echo "")
    [[ -n "$IP" ]] && echo "$IP" && return
  fi

  # EC2 IMDSv1 방식 (fallback)
  IP=$(curl -sf --max-time 2 \
    http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || echo "")
  [[ -n "$IP" ]] && echo "$IP" && return

  # 로컬 IP (EC2 아닌 환경)
  IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "")
  [[ -n "$IP" ]] && echo "$IP" && return

  echo "localhost"
}

HOST_IP=$(detect_ip)
GRAFANA_URL="http://${HOST_IP}"   # 포트 80이므로 생략

# ── 종료 커맨드 ──────────────────────────────────────────────
if [[ "${1:-}" == "stop" ]]; then
  echo "🛑  모니터링 스택 종료..."
  docker compose down
  echo "✅  완료"
  exit 0
fi

echo ""
echo "╔═══════════════════════════════════════════════╗"
echo "║   k6 Load Test + Grafana Monitoring Stack     ║"
echo "╚═══════════════════════════════════════════════╝"
echo ""
echo "  URL      : ${URL}"
echo "  Rate     : ${RATE} req/s  (~$((RATE * 3600)) req/h)"
echo "  Duration : ${DURATION}"
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
  -e DURATION="${DURATION}" \
  k6/script.js

echo ""
echo "✅  테스트 완료. Grafana에서 결과를 확인하세요:"
echo "    ${GRAFANA_URL}"
echo ""
echo "  모니터링 스택을 종료하려면: ./run.sh stop"
