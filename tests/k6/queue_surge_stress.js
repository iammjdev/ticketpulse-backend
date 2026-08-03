// High-RPS virtual queue surge: ramp -> peak burst -> cool down against
// POST /api/v1/queue/join. Each VU is its own JWT-authenticated "user" joining the queue
// repeatedly for the stage duration.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.2/index.js';
import { mintToken } from './lib/jwt.js';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const EVENT_ID = __ENV.EVENT_ID || '22222222-2222-2222-2222-222222222222';
// 10,000 concurrent VUs in a single k6 process needs real headroom (RAM + open file
// descriptors) on the load-gen host. Override PEAK_VUS down (e.g. 2000) on smaller machines.
const PEAK_VUS = Number(__ENV.PEAK_VUS) || 10000;

const serverErrors = new Counter('server_errors');

// Anything other than 200 counts toward http_req_failed so the rate<0.01 threshold means
// what the blueprint says it means.
http.setResponseCallback(http.expectedStatuses(200));

export const options = {
  scenarios: {
    queue_surge: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 2000 },
        { duration: '20s', target: PEAK_VUS },
        { duration: '10s', target: 0 },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<30', 'p(99)<50'],
    http_req_failed: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

export function setup() {
  const healthRes = http.get(`${BASE_URL}/health`);
  if (healthRes.status !== 200) {
    throw new Error(`setup: backend not healthy at ${BASE_URL}/health (status ${healthRes.status})`);
  }
  return { eventId: EVENT_ID };
}

export default function (data) {
  const sub = uuidv4();
  const token = mintToken(sub, 'USER', `${sub}@loadtest.local`);
  const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
  const payload = JSON.stringify({ event_id: data.eventId, user_id: sub });

  const res = http.post(`${BASE_URL}/api/v1/queue/join`, payload, {
    headers,
    tags: { name: 'QueueJoin' },
  });

  check(res, { 'queue join succeeded': (r) => r.status === 200 });
  if (res.status >= 500) {
    serverErrors.add(1);
  }
}

export function handleSummary(data) {
  const m = data.metrics;
  const allThresholds = Object.values(m)
    .flatMap((metric) => (metric.thresholds ? Object.values(metric.thresholds) : []));
  const thresholdsPassed = allThresholds.length > 0 && allThresholds.every((t) => t.ok);

  const report = {
    test: 'queue_surge_stress',
    timestamp: new Date().toISOString(),
    total_requests: m.http_reqs ? m.http_reqs.values.count : 0,
    rps: m.http_reqs ? m.http_reqs.values.rate : 0,
    latency_ms: {
      p90: m.http_req_duration.values['p(90)'],
      p95: m.http_req_duration.values['p(95)'],
      p99: m.http_req_duration.values['p(99)'],
      max: m.http_req_duration.values.max,
    },
    http_req_failed_rate: m.http_req_failed ? m.http_req_failed.values.rate : 0,
    server_errors: m.server_errors ? m.server_errors.values.count : 0,
    thresholds_passed: thresholdsPassed,
  };

  return {
    'results/queue_surge_summary.json': JSON.stringify(report, null, 2),
    stdout: textSummary(data, { indent: ' ', enableColors: true }) + '\n',
  };
}
