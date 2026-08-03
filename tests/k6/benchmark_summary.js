#!/usr/bin/env node
// Reads the JSON extracts each k6 script's handleSummary() writes to results/ and renders
// them into one Markdown report. Run after both stress tests: node benchmark_summary.js
'use strict';

const fs = require('fs');
const path = require('path');

const RESULTS_DIR = path.join(__dirname, 'results');
const OUTPUT_FILE = path.join(__dirname, 'BENCHMARK_RESULTS.md');

function readReport(filename) {
  const filePath = path.join(RESULTS_DIR, filename);
  if (!fs.existsSync(filePath)) {
    console.error(`Missing report: ${filePath} (did that k6 run fail before handleSummary ran?)`);
    return null;
  }
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function fmt(n, digits = 2) {
  return typeof n === 'number' ? n.toFixed(digits) : 'N/A';
}

const seatLock = readReport('seat_lock_summary.json');
const queueSurge = readReport('queue_surge_summary.json');

const maxRps = Math.max(seatLock ? seatLock.rps : 0, queueSurge ? queueSurge.rps : 0);
const raceStatus = seatLock ? (seatLock.race_condition_pass ? 'PASS' : 'FAIL') : 'NOT RUN';

const lines = [];
lines.push('# TicketPulse k6 Benchmark Results');
lines.push('');
lines.push(`_Generated: ${new Date().toISOString()}_`);
lines.push('');
lines.push('## Summary');
lines.push('');
lines.push('| Metric | Value |');
lines.push('| --- | --- |');
lines.push(`| Max RPS Achieved | ${fmt(maxRps)} req/s |`);
lines.push(`| P90 Latency (seat lock) | ${fmt(seatLock && seatLock.latency_ms.p90)} ms |`);
lines.push(`| P95 Latency (seat lock) | ${fmt(seatLock && seatLock.latency_ms.p95)} ms |`);
lines.push(`| P99 Latency (seat lock) | ${fmt(seatLock && seatLock.latency_ms.p99)} ms |`);
lines.push(`| P90 Latency (queue surge) | ${fmt(queueSurge && queueSurge.latency_ms.p90)} ms |`);
lines.push(`| P95 Latency (queue surge) | ${fmt(queueSurge && queueSurge.latency_ms.p95)} ms |`);
lines.push(`| P99 Latency (queue surge) | ${fmt(queueSurge && queueSurge.latency_ms.p99)} ms |`);
lines.push(`| Race Condition Test (Zero Double-Booking) | ${raceStatus} |`);
lines.push('');

lines.push('## Seat Lock Concurrency Test');
lines.push('');
if (seatLock) {
  lines.push('| Field | Value |');
  lines.push('| --- | --- |');
  lines.push(`| Total Requests | ${seatLock.total_requests} |`);
  lines.push(`| Locks Granted (expect exactly 1) | ${seatLock.lock_granted} |`);
  lines.push(`| Seat Already Taken (409) | ${seatLock.seat_taken} |`);
  lines.push(`| Server Errors (500) | ${seatLock.server_errors} |`);
  lines.push(`| Unexpected Status Codes | ${seatLock.unexpected_status} |`);
} else {
  lines.push('_No data - test did not complete._');
}
lines.push('');

lines.push('## Queue Surge Test');
lines.push('');
if (queueSurge) {
  lines.push('| Field | Value |');
  lines.push('| --- | --- |');
  lines.push(`| Total Requests | ${queueSurge.total_requests} |`);
  lines.push(`| Failed Request Rate | ${fmt(queueSurge.http_req_failed_rate * 100)}% |`);
  lines.push(`| Server Errors (500) | ${queueSurge.server_errors} |`);
  lines.push(`| Thresholds Passed | ${queueSurge.thresholds_passed ? 'YES' : 'NO'} |`);
} else {
  lines.push('_No data - test did not complete._');
}
lines.push('');

fs.writeFileSync(OUTPUT_FILE, lines.join('\n'));
console.log(`Benchmark summary written to ${OUTPUT_FILE}`);
