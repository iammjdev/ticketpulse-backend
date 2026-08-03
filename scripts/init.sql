-- Enable UUID extensions for secure and non-enumerable primary keys
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ==========================================
-- ENUM TYPES SETUP
-- ==========================================
CREATE TYPE ticket_status AS ENUM ('AVAILABLE', 'HELD', 'RESERVED', 'SOLD');
CREATE TYPE order_status AS ENUM ('PENDING', 'COMPLETED', 'CHECKED_IN', 'CANCELLED', 'EXPIRED');
CREATE TYPE seat_type AS ENUM ('SEATED', 'STANDING');

DO $$ BEGIN
    CREATE TYPE user_role AS ENUM ('USER', 'ADMIN', 'GATE_STAFF');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE event_status AS ENUM ('UPCOMING', 'PRE_WAITING', 'LIVE', 'ENDED', 'CANCELLED');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- ==========================================
-- USERS & AUTHENTICATION
-- ==========================================
CREATE TABLE IF NOT EXISTS users (
                        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                        email VARCHAR(255) UNIQUE NOT NULL,
                        password_hash VARCHAR(255) NOT NULL,
                        full_name VARCHAR(100),
                        phone VARCHAR(20),
                        national_id VARCHAR(20),
                        role user_role DEFAULT 'USER'::user_role NOT NULL,
                        member_tier VARCHAR(20) DEFAULT 'REGULAR' NOT NULL,
                        is_verified BOOLEAN DEFAULT FALSE NOT NULL,
                        is_suspended BOOLEAN DEFAULT FALSE NOT NULL,
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
                        updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
                        deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- Real login audit trail — recorded on every login attempt (success and failure) by
-- AuthService.Login, backing the admin user directory's audit log drawer.
CREATE TABLE IF NOT EXISTS login_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip_address VARCHAR(64),
    user_agent TEXT,
    success BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_login_events_user ON login_events(user_id, created_at DESC);

-- Seed test accounts (bcrypt cost 10) — pre-verified so the demo logins work out of the box
INSERT INTO users (email, password_hash, full_name, role, member_tier, is_verified)
VALUES
    ('admin@ticketpulse.com', '$2a$10$MUEyT/pHC.t0iufz8OcKfe4PXPQG62jTz3lZSPju.mK6b516wjNme', 'TicketPulse Admin', 'ADMIN', 'REGULAR', TRUE),
    ('user@ticketpulse.com', '$2a$10$dGBTl3iOqhSMxnuWP6KOpu1lHriGhIzWTR3uVIqOJhw.pyUSUfsnq', 'Demo User', 'USER', 'REGULAR', TRUE)
ON CONFLICT (email) DO NOTHING;

-- ==========================================
-- EVENTS & VENUES TABLES
-- ==========================================
CREATE TABLE venues (
                        id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                        name VARCHAR(255) NOT NULL,
                        address TEXT NOT NULL,
                        capacity INT NOT NULL,
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE events (
                        id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                        venue_id UUID NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
                        title VARCHAR(255) NOT NULL,
                        description TEXT,
                        -- TEXT, not VARCHAR(512): the admin banner uploader can store a base64
                        -- data URI here (no object storage wired up in this project), which
                        -- easily exceeds 512 chars for a real image.
                        poster_url TEXT,
                        -- JSON blob: { overview, lineup[], schedule[], rules, faq[] } — optional
                        -- Step 4 "Rich Content & Media" wizard data, rendered as tabs on the
                        -- public event page. Empty string means no rich content set yet.
                        description_rich TEXT NOT NULL DEFAULT '',
                        event_date TIMESTAMPTZ NOT NULL,
                        sale_start_date TIMESTAMPTZ NOT NULL,
                        sale_end_date TIMESTAMPTZ NOT NULL,
                        requires_id_verification BOOLEAN NOT NULL DEFAULT FALSE,
                        status event_status NOT NULL DEFAULT 'UPCOMING',
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                        updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                        deleted_at TIMESTAMPTZ
);

ALTER TABLE events ADD COLUMN IF NOT EXISTS description_rich TEXT NOT NULL DEFAULT '';

-- ==========================================
-- CATEGORIES (Dynamic Category & Venue Management)
-- ==========================================
CREATE TABLE IF NOT EXISTS categories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL UNIQUE,
    slug VARCHAR(120) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_categories_slug ON categories(slug);

INSERT INTO categories (id, name, slug) VALUES
    ('b1111111-1111-1111-1111-111111111111', 'Concert', 'concert'),
    ('b2222222-2222-2222-2222-222222222222', 'Sports', 'sports'),
    ('b3333333-3333-3333-3333-333333333333', 'Theatre & Arts', 'theatre-arts'),
    ('b4444444-4444-4444-4444-444444444444', 'Festival', 'festival'),
    ('b5555555-5555-5555-5555-555555555555', 'Workshop', 'workshop'),
    ('b6666666-6666-6666-6666-666666666666', 'Exhibition', 'exhibition')
ON CONFLICT (id) DO NOTHING;

-- Extend venues with city/map_url (idempotent — safe to re-run against an existing DB).
ALTER TABLE venues ADD COLUMN IF NOT EXISTS city VARCHAR(255);
ALTER TABLE venues ADD COLUMN IF NOT EXISTS map_url TEXT;

-- Backfill the 4 seeded venues on databases that already had this table before this change.
UPDATE venues SET city = 'Bangkok', map_url = 'https://maps.google.com/?q=Rajamangala+National+Stadium' WHERE id = 'a1111111-1111-1111-1111-111111111111' AND city IS NULL;
UPDATE venues SET city = 'Nonthaburi', map_url = 'https://maps.google.com/?q=Impact+Arena+Muang+Thong+Thani' WHERE id = 'a2222222-2222-2222-2222-222222222222' AND city IS NULL;
UPDATE venues SET city = 'Bangkok', map_url = 'https://maps.google.com/?q=Thailand+Cultural+Centre' WHERE id = 'a3333333-3333-3333-3333-333333333333' AND city IS NULL;
UPDATE venues SET city = 'Chonburi', map_url = 'https://maps.google.com/?q=The+Fields+Siam+Country+Club' WHERE id = 'a4444444-4444-4444-4444-444444444444' AND city IS NULL;

-- Add category_id to events, nullable and ON DELETE SET NULL — category is optional metadata,
-- unlike venue_id, so deleting a category should never block or cascade into event deletion.
ALTER TABLE events ADD COLUMN IF NOT EXISTS category_id UUID REFERENCES categories(id) ON DELETE SET NULL;

-- Backfill the 6 seeded events with a sensible category for demo realism.
UPDATE events SET category_id = 'b1111111-1111-1111-1111-111111111111' WHERE id IN ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222','66666666-6666-6666-6666-666666666666') AND category_id IS NULL;
UPDATE events SET category_id = 'b2222222-2222-2222-2222-222222222222' WHERE id = '33333333-3333-3333-3333-333333333333' AND category_id IS NULL;
UPDATE events SET category_id = 'b3333333-3333-3333-3333-333333333333' WHERE id = '44444444-4444-4444-4444-444444444444' AND category_id IS NULL;
UPDATE events SET category_id = 'b4444444-4444-4444-4444-444444444444' WHERE id = '55555555-5555-5555-5555-555555555555' AND category_id IS NULL;

-- Seed demo venues & events — ids match the static catalog in
-- ticketpulse-frontend/src/lib/events.ts so reservations/orders resolve correctly.
-- NOVA BLACK's World Tour (high-demand) requires ID verification before booking.
INSERT INTO venues (id, name, address, capacity)
VALUES
    ('a1111111-1111-1111-1111-111111111111', 'Rajamangala National Stadium', 'Bangkok, Thailand', 65000),
    ('a2222222-2222-2222-2222-222222222222', 'Impact Arena, Muang Thong Thani', 'Nonthaburi, Thailand', 12000),
    ('a3333333-3333-3333-3333-333333333333', 'Thailand Cultural Centre', 'Bangkok, Thailand', 2000),
    ('a4444444-4444-4444-4444-444444444444', 'The Fields, Siam Country Club', 'Chonburi, Thailand', 20000)
ON CONFLICT (id) DO NOTHING;

INSERT INTO events (id, venue_id, title, description, event_date, sale_start_date, sale_end_date, requires_id_verification)
VALUES
    ('11111111-1111-1111-1111-111111111111', 'a1111111-1111-1111-1111-111111111111', 'NOVA BLACK — World Tour in Bangkok', 'High-demand world tour stop — identity verification required to curb scalping.', '2026-09-26T19:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-09-26T18:00:00+07:00', TRUE),
    ('22222222-2222-2222-2222-222222222222', 'a2222222-2222-2222-2222-222222222222', 'COLDPLAY — Music of the Spheres', NULL, '2026-11-14T19:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-11-14T18:00:00+07:00', FALSE),
    ('33333333-3333-3333-3333-333333333333', 'a1111111-1111-1111-1111-111111111111', 'Muay Thai Legends Grand Final', NULL, '2026-10-05T19:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-10-05T18:00:00+07:00', FALSE),
    ('44444444-4444-4444-4444-444444444444', 'a3333333-3333-3333-3333-333333333333', 'The Lion King — Bangkok Engagement', NULL, '2026-12-12T19:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-12-20T18:00:00+07:00', FALSE),
    ('55555555-5555-5555-5555-555555555555', 'a4444444-4444-4444-4444-444444444444', 'Wonderfruit Festival 2026', NULL, '2026-12-11T12:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-12-14T18:00:00+07:00', FALSE),
    ('66666666-6666-6666-6666-666666666666', 'a3333333-3333-3333-3333-333333333333', 'Studio Ghibli: Art of the Score', NULL, '2026-11-01T19:00:00+07:00', '2026-06-01T10:00:00+07:00', '2026-11-01T18:00:00+07:00', FALSE)
ON CONFLICT (id) DO NOTHING;

-- All events are on sale except the Lion King run, which stays UPCOMING (matches its
-- original "coming-soon" queue state in the frontend's prior static catalog).
UPDATE events SET status = 'LIVE'
WHERE id IN (
    '11111111-1111-1111-1111-111111111111',
    '22222222-2222-2222-2222-222222222222',
    '33333333-3333-3333-3333-333333333333',
    '55555555-5555-5555-5555-555555555555',
    '66666666-6666-6666-6666-666666666666'
);

-- ==========================================
-- SEAT ZONES & INVENTORY TRACKING
-- ==========================================
CREATE TABLE seat_zones (
                            id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                            event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
                            zone_name VARCHAR(50) NOT NULL, -- e.g., 'VIP-A', 'AL', 'CAT1'
                            seat_type seat_type NOT NULL DEFAULT 'SEATED',
                            price DECIMAL(10, 2) NOT NULL CHECK (price >= 0),
                            total_capacity INT NOT NULL CHECK (total_capacity > 0),
                            available_stock INT NOT NULL CHECK (available_stock >= 0),
                            created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                            UNIQUE(event_id, zone_name)
);

-- Multi-tier ticket inventory per event — available_stock mirrors total_capacity at seed
-- time; live availability is tracked in Redis (see WarmupStock/ReserveTicket).
INSERT INTO seat_zones (event_id, zone_name, seat_type, price, total_capacity, available_stock)
VALUES
    ('11111111-1111-1111-1111-111111111111', 'VIP Standing', 'STANDING', 9800, 500, 500),
    ('11111111-1111-1111-1111-111111111111', 'CAT 1 Seated', 'SEATED', 6500, 2000, 2000),
    ('11111111-1111-1111-1111-111111111111', 'CAT 2 Seated', 'SEATED', 2800, 4000, 4000),

    ('22222222-2222-2222-2222-222222222222', 'VIP Standing', 'STANDING', 12500, 400, 400),
    ('22222222-2222-2222-2222-222222222222', 'CAT 1 Seated', 'SEATED', 7500, 1800, 1800),
    ('22222222-2222-2222-2222-222222222222', 'CAT 2 Seated', 'SEATED', 3500, 3500, 3500),

    ('33333333-3333-3333-3333-333333333333', 'Ringside', 'SEATED', 5000, 200, 200),
    ('33333333-3333-3333-3333-333333333333', 'CAT 1 Seated', 'SEATED', 2800, 1000, 1000),
    ('33333333-3333-3333-3333-333333333333', 'General Seated', 'SEATED', 1200, 2500, 2500),

    ('44444444-4444-4444-4444-444444444444', 'VIP Seated', 'SEATED', 6500, 150, 150),
    ('44444444-4444-4444-4444-444444444444', 'CAT 1 Seated', 'SEATED', 3800, 500, 500),
    ('44444444-4444-4444-4444-444444444444', 'CAT 2 Seated', 'SEATED', 1500, 900, 900),

    ('55555555-5555-5555-5555-555555555555', 'VIP GA', 'STANDING', 15900, 600, 600),
    ('55555555-5555-5555-5555-555555555555', 'CAT 1 GA', 'STANDING', 8500, 2000, 2000),
    ('55555555-5555-5555-5555-555555555555', 'General GA', 'STANDING', 4200, 5000, 5000),

    ('66666666-6666-6666-6666-666666666666', 'VIP Seated', 'SEATED', 4500, 150, 150),
    ('66666666-6666-6666-6666-666666666666', 'CAT 1 Seated', 'SEATED', 2500, 500, 500),
    ('66666666-6666-6666-6666-666666666666', 'CAT 2 Seated', 'SEATED', 1000, 900, 900)
ON CONFLICT (event_id, zone_name) DO NOTHING;

CREATE TABLE tickets (
                         id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                         event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
                         zone_id UUID NOT NULL REFERENCES seat_zones(id) ON DELETE CASCADE,
                         seat_number VARCHAR(50) NOT NULL, -- e.g., 'A-12' for seated or 'ST-0042' for standing
                         status ticket_status NOT NULL DEFAULT 'AVAILABLE',
                         held_by_user_id UUID,
                         held_until TIMESTAMPTZ, -- Expiration timestamp for temporary holds (e.g., 10 mins)
                         version INT NOT NULL DEFAULT 1, -- For Optimistic Locking in DB layer
                         created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                         updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                         UNIQUE(event_id, seat_number)
);

-- ==========================================
-- SEAT MAP LAYOUT (Dynamic Seat Map Engine — Phase 1)
-- ==========================================
-- Physical seat coordinates for interactive venue rendering. Distinct from `tickets`
-- (inventory/reservation ledger) — a row here is a point on the map, not a sellable unit;
-- live HELD/SOLD status is tracked in Redis (event:{eventId}:seat_status), not here.
CREATE TABLE IF NOT EXISTS seats (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id UUID NOT NULL REFERENCES seat_zones(id) ON DELETE CASCADE,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    row_label VARCHAR(10) NOT NULL,
    seat_number INT NOT NULL,
    position_x FLOAT NOT NULL DEFAULT 0,
    position_y FLOAT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT unique_event_seat UNIQUE(event_id, row_label, seat_number)
);
CREATE INDEX IF NOT EXISTS idx_seats_event_zone ON seats(event_id, zone_id);

-- ==========================================
-- 4. ORDERS & TRANSACTION HISTORY
-- ==========================================
CREATE TABLE orders (
                        id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                        user_id UUID NOT NULL,
                        event_id UUID NOT NULL REFERENCES events(id),
                        zone_id VARCHAR(64), -- seat-zone slug (e.g. 'cat1-west'), not FK'd — inventory lives in Redis
                        quantity INT NOT NULL DEFAULT 1,
                        total_amount DECIMAL(10, 2) NOT NULL,
                        status order_status NOT NULL DEFAULT 'PENDING',
                        idempotency_key VARCHAR(255) UNIQUE NOT NULL, -- To prevent duplicate payments
                        expires_at TIMESTAMPTZ NOT NULL,
                        checked_in_at TIMESTAMPTZ,
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                        updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE order_items (
                             id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                             order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
                             ticket_id UUID NOT NULL REFERENCES tickets(id),
                             price DECIMAL(10, 2) NOT NULL,
                             created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

-- ==========================================
-- NEWS & ANNOUNCEMENTS CMS
-- ==========================================
DO $$ BEGIN
    CREATE TYPE news_category AS ENUM ('ANNOUNCEMENT', 'CONCERT_NEWS', 'PROMOTION', 'TICKET_ALERT');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

CREATE TABLE news_articles (
                        id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                        title VARCHAR(255) NOT NULL,
                        slug VARCHAR(255) UNIQUE NOT NULL,
                        summary TEXT NOT NULL,
                        content TEXT NOT NULL,
                        cover_image TEXT,
                        category news_category NOT NULL DEFAULT 'ANNOUNCEMENT',
                        is_published BOOLEAN NOT NULL DEFAULT TRUE,
                        views_count INT NOT NULL DEFAULT 0,
                        published_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
                        created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
                        updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_news_published_list ON news_articles(is_published, published_at DESC);
CREATE INDEX idx_news_category ON news_articles(category);

INSERT INTO news_articles (title, slug, summary, content, cover_image, category, is_published, views_count, published_at)
VALUES
    (
        'TicketPulse Gate Scanner Upgrade Notice',
        'ticketpulse-gate-scanner-upgrade-notice',
        'We are rolling out a faster HMAC-verified QR scanner across all venue gates starting this month — expect shorter entry lines.',
        E'## Faster Gates, Zero Downtime\n\nStarting this month, every TicketPulse venue gate is switching to our next-generation scanner hardware. The new devices verify each ticket''s HMAC-SHA256 signature locally before round-tripping to the server, cutting average scan time from 800ms to under 120ms.\n\n**What this means for you:**\n\n- Shorter lines at VIP and general admission gates\n- Offline-tolerant scanning during brief network blips\n- The same dynamic QR code in your wallet — no re-download needed\n\nGate staff have already been trained on the new flow ahead of the Coldplay and NOVA BLACK tour dates. Thanks for your patience during the rollout.',
        'https://images.unsplash.com/photo-1540039155733-5bb30b53aa14?w=1200',
        'ANNOUNCEMENT',
        TRUE,
        1842,
        '2026-07-18T09:00:00+07:00'
    ),
    (
        'Coldplay World Tour: Extra CAT 2 Zone Released',
        'coldplay-world-tour-extra-cat2-zone-released',
        'Due to overwhelming demand, we have unlocked an additional 1,500 CAT 2 seats for the Music of the Spheres Bangkok stop.',
        E'## Extra Capacity Unlocked\n\nAfter CAT 2 sold out within nine minutes of going live, the promoter has approved an additional release of 1,500 seats for **COLDPLAY — Music of the Spheres** at Impact Arena.\n\nThe new allocation is now live in the Redis stock pool and reflected instantly on the event page — no refresh trickery needed, the zone counter updates in real time as tickets are reserved.\n\nJoin the virtual queue early; based on the first release, we expect this batch to move just as fast.',
        'https://images.unsplash.com/photo-1470229722913-7c0e2dbbafd3?w=1200',
        'CONCERT_NEWS',
        TRUE,
        5390,
        '2026-07-22T14:30:00+07:00'
    ),
    (
        'August Promo: 10% Off VIP Tiers with Code PULSE10',
        'august-promo-10-percent-off-vip-tiers',
        'Book any VIP or VIP Standing zone this August and save 10% at checkout with code PULSE10 — valid on all live events.',
        E'## Limited-Time VIP Discount\n\nTo celebrate the platform''s biggest month yet, every **VIP** and **VIP Standing** zone across all currently on-sale events is 10% off when you apply code `PULSE10` at checkout.\n\n**Terms:**\n\n- Valid August 1–31, 2026\n- Stacks with member-tier pricing where applicable\n- One redemption per order, non-transferable\n\nApplies automatically to eligible zones — just make sure your cart total reflects the discount before confirming payment.',
        'https://images.unsplash.com/photo-1493225457124-a3eb161ffa5f?w=1200',
        'PROMOTION',
        TRUE,
        3127,
        '2026-08-01T08:00:00+07:00'
    ),
    (
        'Ticket Alert: NOVA BLACK Requires ID Verification at Checkout',
        'nova-black-requires-id-verification-checkout',
        'Reminder: NOVA BLACK — World Tour tickets require a verified National ID or Passport on file before reservation completes.',
        E'## Identity Verification Reminder\n\nTo curb scalping on our highest-demand show, **NOVA BLACK — World Tour in Bangkok** requires a verified National ID or Passport on your account before any reservation can be confirmed.\n\nIf you haven''t verified yet, you''ll be prompted during checkout — the process takes under a minute and only needs to be completed once. Your ID name must match the name on your ticket at the gate.\n\nVerify ahead of time from **Settings → Account Details** to skip the extra step when the queue opens.',
        'https://images.unsplash.com/photo-1501281668745-f7f57925c3b4?w=1200',
        'TICKET_ALERT',
        TRUE,
        2764,
        '2026-07-25T11:15:00+07:00'
    )
ON CONFLICT (slug) DO NOTHING;

-- ==========================================
-- HIGH-PERFORMANCE INDEXES (CS Optimization)
-- ==========================================
-- B-Tree Compound Index for instant zone stock checks ($O(\log N)$ complexity)
CREATE INDEX idx_tickets_event_zone_status ON tickets(event_id, zone_id, status);

-- Partial Index for fast cleanup of expired holds
CREATE INDEX idx_tickets_expired_holds ON tickets(held_until) WHERE status = 'HELD';

-- Index for User Order History lookups
CREATE INDEX idx_orders_user_status ON orders(user_id, status);