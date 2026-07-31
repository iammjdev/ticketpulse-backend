-- Enable UUID extensions for secure and non-enumerable primary keys
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ==========================================
-- ENUM TYPES SETUP
-- ==========================================
CREATE TYPE ticket_status AS ENUM ('AVAILABLE', 'HELD', 'RESERVED', 'SOLD');
CREATE TYPE order_status AS ENUM ('PENDING', 'COMPLETED', 'CANCELLED', 'EXPIRED');
CREATE TYPE seat_type AS ENUM ('SEATED', 'STANDING');

DO $$ BEGIN
    CREATE TYPE user_role AS ENUM ('USER', 'ADMIN', 'GATE_STAFF');
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
                        full_name VARCHAR(100) NOT NULL,
                        phone VARCHAR(20),
                        national_id VARCHAR(20),
                        role user_role DEFAULT 'USER'::user_role NOT NULL,
                        member_tier VARCHAR(20) DEFAULT 'REGULAR' NOT NULL,
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
                        updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- Seed test accounts (bcrypt cost 10)
INSERT INTO users (email, password_hash, full_name, role, member_tier)
VALUES
    ('admin@ticketpulse.com', '$2a$10$sGnj2rjxgq51mW8SsvS0xuRzDi40l1OFe.O9WHVgE7G.ReRRP0WUa', 'TicketPulse Admin', 'ADMIN', 'REGULAR'),
    ('user@ticketpulse.com', '$2a$10$dGBTl3iOqhSMxnuWP6KOpu1lHriGhIzWTR3uVIqOJhw.pyUSUfsnq', 'Demo User', 'USER', 'REGULAR')
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
                        poster_url VARCHAR(512),
                        event_date TIMESTAMPTZ NOT NULL,
                        sale_start_date TIMESTAMPTZ NOT NULL,
                        sale_end_date TIMESTAMPTZ NOT NULL,
                        created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
                        updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
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
-- 4. ORDERS & TRANSACTION HISTORY
-- ==========================================
CREATE TABLE orders (
                        id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                        user_id UUID NOT NULL,
                        event_id UUID NOT NULL REFERENCES events(id),
                        total_amount DECIMAL(10, 2) NOT NULL,
                        status order_status NOT NULL DEFAULT 'PENDING',
                        idempotency_key VARCHAR(255) UNIQUE NOT NULL, -- To prevent duplicate payments
                        expires_at TIMESTAMPTZ NOT NULL,
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
-- HIGH-PERFORMANCE INDEXES (CS Optimization)
-- ==========================================
-- B-Tree Compound Index for instant zone stock checks ($O(\log N)$ complexity)
CREATE INDEX idx_tickets_event_zone_status ON tickets(event_id, zone_id, status);

-- Partial Index for fast cleanup of expired holds
CREATE INDEX idx_tickets_expired_holds ON tickets(held_until) WHERE status = 'HELD';

-- Index for User Order History lookups
CREATE INDEX idx_orders_user_status ON orders(user_id, status);