// Atomic seat-lock concurrency test: 1,000 VUs fire on the SAME event_id/seat_id in one
// burst. Exactly one request must win the Redis Lua lock (reserve_specific_seat.lua);
// every other request must be told the seat is taken. Any 500 is a bug.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.0.2/index.js';
import { mintToken } from './lib/jwt.js';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const EVENT_ID = __ENV.EVENT_ID || '22222222-2222-2222-2222-222222222222'; // Coldplay: no ID-verification branch
const SEAT_ROW = __ENV.SEAT_ROW || 'A';
const SEAT_NUMBER = Number(__ENV.SEAT_NUMBER) || 12;
const VUS = Number(__ENV.VUS) || 1000;

const lockGranted = new Counter('lock_granted');
const seatTaken = new Counter('seat_taken');
const serverErrors = new Counter('server_errors');
const unexpectedStatus = new Counter('unexpected_status');

export const options = {
  scenarios: {
    seat_lock_race: {
      executor: 'per-vu-iterations',
      vus: VUS,
      iterations: 1,
      maxDuration: '30s',
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<50'],
    server_errors: ['count<=0'],
    lock_granted: ['count<=1'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

// Seeds one real seat row so FindSeatForReservation (Postgres) has something to find before
// the Redis lock is even attempted — without this every VU 404s and the race never happens.
export function setup() {
  const adminToken = mintToken(
    '00000000-0000-0000-0000-000000000000',
    'ADMIN',
    'loadtest-admin@ticketpulse.local'
  );
  const adminHeaders = { 'Content-Type': 'application/json', Authorization: `Bearer ${adminToken}` };

  const eventRes = http.get(`${BASE_URL}/api/v1/events/${EVENT_ID}`);
  if (eventRes.status !== 200) {
    throw new Error(
      `setup: GET /events/${EVENT_ID} returned ${eventRes.status}. Seed the DB first (docker-compose up runs scripts/init.sql).`
    );
  }
  const zones = eventRes.json('event.zones') || [];
  if (zones.length === 0) {
    throw new Error(`setup: event ${EVENT_ID} has no zones to attach a seat to.`);
  }
  const zoneId = zones[0].id;

  const createRes = http.post(
    `${BASE_URL}/api/v1/admin/events/${EVENT_ID}/seats`,
    JSON.stringify({ seats: [{ zone_id: zoneId, row_label: SEAT_ROW, seat_number: SEAT_NUMBER }] }),
    { headers: adminHeaders }
  );
  if (createRes.status !== 201) {
    throw new Error(`setup: seat creation failed (${createRes.status}): ${createRes.body}`);
  }

  const seatsRes = http.get(`${BASE_URL}/api/v1/events/${EVENT_ID}/seats`);
  const seats = seatsRes.json('seats') || [];
  const target = seats.find((s) => s.row_label === SEAT_ROW && s.seat_number === SEAT_NUMBER);
  if (!target) {
    throw new Error(`setup: seat ${SEAT_ROW}${SEAT_NUMBER} not found after creation`);
  }

  return { eventId: EVENT_ID, seatId: target.id, seatLabel: `${SEAT_ROW}${SEAT_NUMBER}` };
}

export default function (data) {
  const sub = uuidv4();
  const token = mintToken(sub, 'USER', `${sub}@loadtest.local`);
  const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
  const payload = JSON.stringify({ event_id: data.eventId, seat_id: data.seatId });

  const res = http.post(`${BASE_URL}/api/v1/tickets/reserve-seat`, payload, {
    headers,
    tags: { name: 'ReserveSeat' },
  });

  if (res.status >= 200 && res.status < 300) {
    lockGranted.add(1);
    check(res, { 'lock granted (HELD)': (r) => r.json('status') === 'HELD' });
  } else if (res.status === 409) {
    seatTaken.add(1);
    check(res, { 'seat reported taken': (r) => r.json('status') === 'TAKEN' });
  } else if (res.status >= 500) {
    serverErrors.add(1);
  } else {
    unexpectedStatus.add(1);
  }
}

export function handleSummary(data) {
  const m = data.metrics;
  const count = (name) => (m[name] ? m[name].values.count : 0);
  const lockGrantedCount = count('lock_granted');
  const serverErrorCount = count('server_errors');

  const report = {
    test: 'seat_lock_stress',
    timestamp: new Date().toISOString(),
    total_requests: m.http_reqs ? m.http_reqs.values.count : 0,
    rps: m.http_reqs ? m.http_reqs.values.rate : 0,
    latency_ms: {
      p90: m.http_req_duration.values['p(90)'],
      p95: m.http_req_duration.values['p(95)'],
      p99: m.http_req_duration.values['p(99)'],
      max: m.http_req_duration.values.max,
    },
    lock_granted: lockGrantedCount,
    seat_taken: count('seat_taken'),
    server_errors: serverErrorCount,
    unexpected_status: count('unexpected_status'),
    race_condition_pass: lockGrantedCount === 1 && serverErrorCount === 0,
  };

  return {
    'results/seat_lock_summary.json': JSON.stringify(report, null, 2),
    stdout: textSummary(data, { indent: ' ', enableColors: true }) + '\n',
  };
}
