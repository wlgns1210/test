/**
 * k6 Load Test Script — 패턴 선택 가능
 *
 * PATTERN 환경변수로 트래픽 패턴 선택:
 *
 *   rampup (기본)
 *     0 → RATE 점진 증가 → 유지 → 감소
 *     변수: RATE, RAMPUP(2m), SUSTAIN(3m), RAMPDOWN(1m)
 *
 *   step
 *     RATE를 STEPS 단계로 나눠 계단식 증가
 *     변수: RATE, STEPS(4), STEP_DURATION(1m), SUSTAIN(2m)
 *
 *   spike
 *     낮은 기준 트래픽 → 갑작스러운 급등 → 기준 복귀
 *     변수: RATE, BASELINE(5), WARMUP(1m), SPIKE_DURATION(30s), COOLDOWN(1m)
 *
 * 공통 변수:
 *   URL  - 대상 URL (default: http://localhost:8080/v1/stress)
 *   RATE - 피크 req/s (default: 56)
 */

import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

const customLatency = new Trend('stress_latency_ms', true);
const customSuccess = new Rate('stress_success_rate');
const customTotal   = new Counter('stress_total_requests');

// ── 공통 변수 ─────────────────────────────────────────────────
const TARGET_URL = __ENV.URL     || 'http://localhost:8080/v1/stress';
const RATE       = parseInt(__ENV.RATE || '56');
const PATTERN    = (__ENV.PATTERN || 'rampup').toLowerCase();

// ── 패턴별 stage 계산 ─────────────────────────────────────────
function buildStages() {
  if (PATTERN === 'step') {
    const steps        = parseInt(__ENV.STEPS         || '4');
    const stepDuration = __ENV.STEP_DURATION          || '1m';
    const sustain      = __ENV.SUSTAIN                || '2m';

    const stages = [];
    for (let i = 1; i <= steps; i++) {
      stages.push({ duration: stepDuration, target: Math.round(RATE * i / steps) });
    }
    stages.push({ duration: sustain, target: RATE }); // 피크 유지
    return stages;
  }

  if (PATTERN === 'spike') {
    const baseline      = parseInt(__ENV.BASELINE       || '5');
    const warmup        = __ENV.WARMUP                  || '1m';
    const spikeDuration = __ENV.SPIKE_DURATION          || '30s';
    const cooldown      = __ENV.COOLDOWN                || '1m';

    return [
      { duration: warmup,        target: baseline },  // 기준 트래픽 워밍업
      { duration: '10s',         target: RATE     },  // 급등 (10초)
      { duration: spikeDuration, target: RATE     },  // 스파이크 유지
      { duration: '10s',         target: baseline },  // 급감 (10초)
      { duration: cooldown,      target: baseline },  // 기준 복귀
    ];
  }

  // rampup (기본값)
  const rampup   = __ENV.RAMPUP   || '2m';
  const sustain  = __ENV.SUSTAIN  || '3m';
  const rampdown = __ENV.RAMPDOWN || '1m';

  return [
    { duration: rampup,   target: RATE },  // 점진 증가
    { duration: sustain,  target: RATE },  // 피크 유지
    { duration: rampdown, target: 0    },  // 점진 감소
  ];
}

export const options = {
  scenarios: {
    stress_test: {
      executor:        'ramping-arrival-rate',
      startRate:       0,
      timeUnit:        '1s',
      preAllocatedVUs: 100,
      maxVUs:          500,
      stages:          buildStages(),
    },
  },

  thresholds: {
    http_req_duration:   ['p(95)<500', 'p(99)<1000'],
    http_req_failed:     ['rate<0.05'],
    stress_success_rate: ['rate>0.95'],
  },
};

// ── 유틸 함수 ─────────────────────────────────────────────────
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

// ── 메인 VU 함수 ──────────────────────────────────────────────
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

// ── 요약 출력 ─────────────────────────────────────────────────
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
  k6 테스트 완료  [패턴: ${PATTERN.toUpperCase()}]
══════════════════════════════════════════════
  RPS (평균)  : ${rps} req/s
  P95 latency : ${p95} ms
  P99 latency : ${p99} ms
  Error rate  : ${failRate} %
══════════════════════════════════════════════`);

  return {};
}
