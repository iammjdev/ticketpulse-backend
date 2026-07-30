import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

// Custom Metrics for In-depth Performance Analysis
const successfulReservations = new Counter('successful_reservations');
const soldOutResponses = new Counter('sold_out_responses');
const failedRequests = new Counter('failed_requests');
const reserveLatency = new Trend('reserve_latency_ms');
const errorRate = new Rate('error_rate');

export const options = {
    // Load test execution stages (Ramp-up -> Peak Traffic -> Ramp-down)
    stages: [
        { duration: '10s', target: 50 },   // Warm-up: 50 Virtual Users (VUs)
        { duration: '20s', target: 200 },  // Ramp-up: 200 VUs
        { duration: '30s', target: 500 },  // Peak Traffic: 500 VUs hammering concurrently
        { duration: '10s', target: 0 },    // Cool-down: 0 VUs
    ],
    thresholds: {
        // Latency targets
        http_req_duration: ['p(95)<200', 'p(99)<500'], // 95% of requests < 200ms, 99% < 500ms
        error_rate: ['rate<0.01'],                    // Unexpected system error rate strictly below 1%
    },
};

// Environment Variables with Fallbacks
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const EVENT_ID = __ENV.EVENT_ID || '11111111-1111-1111-1111-111111111111';

export default function () {
    const userId = uuidv4();
    const headers = { 'Content-Type': 'application/json' };

    group('TicketPulse High-Concurrency Booking Flow', function () {
        // -----------------------------------------------------------------
        // Step 1: Join Virtual Waiting Room Queue
        // -----------------------------------------------------------------
        const joinPayload = JSON.stringify({
            user_id: userId,
            event_id: EVENT_ID,
        });

        const joinRes = http.post(`${BASE_URL}/api/v1/queue/join`, joinPayload, { headers });
        const joinSuccess = check(joinRes, {
            'Queue Join HTTP 200/201': (r) => r.status === 200 || r.status === 201,
        });

        if (!joinSuccess) {
            failedRequests.add(1);
            errorRate.add(1);
            return;
        }

        // -----------------------------------------------------------------
        // Step 2: Attempt Atomic Ticket Reservation (Redis Lua Lock Engine)
        // -----------------------------------------------------------------
        const reservePayload = JSON.stringify({
            user_id: userId,
            event_id: EVENT_ID,
            quantity: 1,
        });

        const startTime = Date.now();
        const reserveRes = http.post(`${BASE_URL}/api/v1/tickets/reserve`, reservePayload, { headers });
        const duration = Date.now() - startTime;
        reserveLatency.add(duration);

        if (reserveRes.status === 200 || reserveRes.status === 201) {
            successfulReservations.add(1);
            errorRate.add(0);
            check(reserveRes, {
                'Ticket Reservation Successful': (r) => r.status === 200 || r.status === 201,
            });
        } else if (reserveRes.status === 400 || reserveRes.status === 409) {
            // Stock exhausted / Event sold out (Valid business state, NOT a server error)
            soldOutResponses.add(1);
            errorRate.add(0);
        } else {
            // Server error (500, 502, timeout, etc.)
            failedRequests.add(1);
            errorRate.add(1);
        }
    });

    // Micro-pause to prevent CPU saturation on the runner client
    sleep(0.05);
}