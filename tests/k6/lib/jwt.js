// Self-signed HS256 JWT minting, matching internal/service/auth_service.go's Claims
// (email, role, sub/exp/iat via jwt.RegisteredClaims) and its jwtSecret() fallback.
// Load-testing reserve-seat/queue.join at 1k-10k VU scale can't afford a real
// register+OTP+login round trip per VU, so we sign tokens with the same secret the
// backend falls back to when JWT_SECRET is unset.
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';

const JWT_SECRET = __ENV.JWT_SECRET || 'ticketpulse_super_secret_jwt_key_2026';

function base64url(input) {
  return encoding.b64encode(input, 'rawurl');
}

export function mintToken(sub, role, email, ttlSeconds = 3600) {
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: 'HS256', typ: 'JWT' };
  const payload = { email, role, sub, exp: now + ttlSeconds, iat: now };

  const signingInput = `${base64url(JSON.stringify(header))}.${base64url(JSON.stringify(payload))}`;
  const signature = crypto.hmac('sha256', JWT_SECRET, signingInput, 'base64rawurl');
  return `${signingInput}.${signature}`;
}
