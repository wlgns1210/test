/**
 * k6 Load Test Script — Ramp-up Pattern
 *
 * 트래픽 패턴:
 *   [RAMPUP]  0 → RATE 까지 점진적 증가
 *   [SUSTAIN] RATE 유지 (피크 구간)
 *   [RAMPDOWN] RATE → 0 으로 점진적 감소
 *
 * 환경변수:
 *   URL      - 대상 URL              (default: http://localhost:8080/v1/stress)
 *   RATE     - 피크 초당 요청 수     (default: 56  ≈ 200,000 req/h)
 *   RAMPUP   - 증가 구간 지속 시간   (default: 2m)
 *   SUSTAIN  - 피크 유지 지속 시간   (default: 3m)
 *   RAMPDOWN - 감소 구간 지속 시간   (default: 1m)
 */

import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

const customLatency = new Trend('stress_latency_ms', true);
const customSuccess = new Rate('stress_success_rate');
const customTotal   = new Counter('stress_total_requests');

const TARGET_URL = __ENV.URL      || 'http://localhost:8080/v1/stress';
const RATE       = parseInt(__ENV.RATE     || '56');
const RAMPUP     = __ENV.RAMPUP   || '2m';
const SUSTAIN    = __ENV.SUSTAIN  || '3m';
const RAMPDOWN   = __ENV.RAMPDOWN || '1m';

export const options = {
  scenarios: {
    stress_test: {
      executor:        'ramping-arrival-rate',
      startRate:       0,           // 시작은 0 req/s
      timeUnit:        '1s',
      preAllocatedVUs: 100,
      maxVUs:          500,
      stages: [
        { duration: RAMPUP,   target: RATE },  // 0 → RATE (점진 증가)
        { duration: SUSTAIN,  target: RATE },  // RATE 유지 (피크)
        { duration: RAMPDOWN, target: 0    },  // RATE → 0 (점진 감소)
      ],
    },
  },

  thresholds: {
    http_req_duration:   ['p(95)<500', 'p(99)<1000'],
    http_req_failed:     ['rate<0.05'],
    stress_success_rate: ['rate>0.95'],
  },
};

function generateUUID() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

function generateRequestId() {
  return String(Math.floor(Math.random() * 1_000_000_000_000)).padStart(12, '0');
}

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

  customLatency.add(res.timings.duration);
  customSuccess.add(ok);
  customTotal.add(1);
}

export function handleSummary(data) {
  const dur   = data.metrics.http_req_duration;
  const reqs  = data.metrics.http_reqs;
  const fails = data.metrics.http_req_failed;

  const rps      = reqs  ? reqs.values.rate.toFixed(2)         : 'N/A';
  const p95      = dur   ? dur.values['p(95)'].toFixed(2)       : 'N/A';
  const p99      = dur   ? dur.values['p(99)'].toFixed(2)       : 'N/A';
  const failRate = fails ? (fails.values.rate * 100).toFixed(2) : 'N/A';

  console.log(`
══════════════════════════════════════════════
  k6 테스트 완료 (Ramp-up 패턴)
══════════════════════════════════════════════
  RPS (평균)  : ${rps} req/s
  P95 latency : ${p95} ms
  P99 latency : ${p99} ms
  Error rate  : ${failRate} %
══════════════════════════════════════════════`);

  return {};
}
