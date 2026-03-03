/**
 * k6 Load Test Script
 * Target: POST /v1/stress  (requestid + uuid 랜덤 생성)
 *
 * 환경변수로 동적 설정 가능:
 *   URL      - 대상 서버 URL     (default: http://localhost:8080/v1/stress)
 *   RATE     - 초당 요청 수       (default: 56  ≈ 200,000 req/h)
 *   DURATION - 테스트 지속 시간   (default: 1m)
 */

import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

// ── 커스텀 메트릭 ────────────────────────────────────────────────
const customLatency  = new Trend('stress_latency_ms', true);
const customSuccess  = new Rate('stress_success_rate');
const customTotal    = new Counter('stress_total_requests');

// ── 환경 변수 ────────────────────────────────────────────────────
const TARGET_URL = __ENV.URL      || 'http://localhost:8080/v1/stress';
const RATE       = parseInt(__ENV.RATE     || '56');   // req/s
const DURATION   = __ENV.DURATION || '1m';

// ── k6 옵션 ─────────────────────────────────────────────────────
export const options = {
  scenarios: {
    stress_test: {
      executor:        'constant-arrival-rate',
      rate:            RATE,          // req/s
      timeUnit:        '1s',
      duration:        DURATION,
      preAllocatedVUs: 100,           // 응답 지연이 길면 자동 증가
      maxVUs:          500,
    },
  },

  // 성공 기준 (위반 시 exit code 1)
  thresholds: {
    http_req_duration:       ['p(95)<500', 'p(99)<1000'],
    http_req_failed:         ['rate<0.05'],
    stress_success_rate:     ['rate>0.95'],
  },
};

// ── 유틸 함수 ────────────────────────────────────────────────────
function generateUUID() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

function generateRequestId() {
  // 12자리 랜덤 숫자 문자열
  return String(Math.floor(Math.random() * 1_000_000_000_000)).padStart(12, '0');
}

// ── 메인 VU 함수 ─────────────────────────────────────────────────
export default function () {
  const payload = JSON.stringify({
    requestid: generateRequestId(),
    uuid:      generateUUID(),
    length:    256,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'User-Agent':   'k6-load-test/1.0',
    },
    tags: { endpoint: 'stress' },
  };

  const res = http.post(TARGET_URL, payload, params);

  const ok = check(res, {
    'HTTP 200':        (r) => r.status === 200,
    'latency < 500ms': (r) => r.timings.duration < 500,
  });

  // 커스텀 메트릭 기록
  customLatency.add(res.timings.duration);
  customSuccess.add(ok);
  customTotal.add(1);
}

// ── 요약 출력 ────────────────────────────────────────────────────
export function handleSummary(data) {
  const dur   = data.metrics.http_req_duration;
  const reqs  = data.metrics.http_reqs;
  const fails = data.metrics.http_req_failed;

  const rps       = reqs  ? reqs.values.rate.toFixed(2)          : 'N/A';
  const p95       = dur   ? dur.values['p(95)'].toFixed(2)        : 'N/A';
  const p99       = dur   ? dur.values['p(99)'].toFixed(2)        : 'N/A';
  const failRate  = fails ? (fails.values.rate * 100).toFixed(2)  : 'N/A';

  console.log(`
══════════════════════════════════════════════
  k6 테스트 완료
══════════════════════════════════════════════
  RPS        : ${rps} req/s
  P95 latency: ${p95} ms
  P99 latency: ${p99} ms
  Error rate : ${failRate} %
══════════════════════════════════════════════`);

  return {};
}
