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
GRAFANA_URL="http://localhost:3000"

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
echo "  Rate     : ${RATE} req/s  (~$(echo "${RATE} * 3600" | bc) req/h)"
echo "  Duration : ${DURATION}"
echo ""

# ── 1. Docker 스택 시작 ──────────────────────────────────────
echo "🐳  모니터링 스택 시작 (InfluxDB + Grafana)..."
docker compose up -d

# ── 2. 서비스 준비 대기 ──────────────────────────────────────
echo "⏳  서비스 준비 중..."
for i in {1..20}; do
  if curl -sf http://localhost:8086/ping > /dev/null 2>&1 && \
     curl -sf "${GRAFANA_URL}/api/health" > /dev/null 2>&1; then
    break
  fi
  sleep 2
done
echo "✅  서비스 준비 완료"
echo ""
echo "📊  Grafana 대시보드: ${GRAFANA_URL}"
echo "     (브라우저에서 열어 실시간 모니터링)"
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
