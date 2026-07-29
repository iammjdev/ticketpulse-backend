-- Enable UUID extension for secure and non-enumerable primary keys
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ==========================================
-- ENUM TYPES SETUP
-- ==========================================
CREATE TYPE ticket_status AS ENUM ('AVAILABLE', 'HELD', 'RESERVED', 'SOLD');
CREATE TYPE order_status AS ENUM ('PENDING', 'COMPLETED', 'CANCELLED', 'EXPIRED');
CREATE TYPE seat_type AS ENUM ('SEATED', 'STANDING');

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