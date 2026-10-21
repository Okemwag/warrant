-- Decisions can replace an earlier one (price changed at checkout).
ALTER TABLE decisions ADD COLUMN supersedes TEXT REFERENCES decisions (id);

-- One PayPal order per decision.
CREATE TABLE paypal_orders (
    order_id          TEXT PRIMARY KEY,
    decision_id       TEXT NOT NULL UNIQUE REFERENCES decisions (id),
    reservation_id    TEXT NOT NULL REFERENCES reservations (id),
    permission_id     TEXT NOT NULL,
    mode              TEXT NOT NULL CHECK (mode IN ('approve', 'vault')),
    invoice_id        TEXT NOT NULL UNIQUE,
    quoted_cents      BIGINT NOT NULL,
    final_cents       BIGINT NOT NULL,
    authorized_cents  BIGINT,
    authorization_id  TEXT,
    capture_id        TEXT UNIQUE,
    status            TEXT NOT NULL CHECK (status IN (
                          'awaiting_buyer', 'authorized', 'captured', 'completed',
                          'voided', 'failed', 'refunded')),
    approve_url       TEXT,
    failure           TEXT,
    debug_id          TEXT,
    refund_id         TEXT,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);
CREATE INDEX paypal_orders_permission ON paypal_orders (permission_id, created_at DESC);

-- The shopper's saved PayPal account (Mode B). Single-user MVP: one row.
CREATE TABLE paypal_vault (
    id             TEXT PRIMARY KEY DEFAULT 'default' CHECK (id = 'default'),
    setup_token_id TEXT,
    vault_id       TEXT,
    status         TEXT NOT NULL CHECK (status IN ('pending', 'connected', 'failed')),
    approve_url    TEXT,
    updated_at     TIMESTAMPTZ NOT NULL
);

-- Every verified webhook, once.
CREATE TABLE webhook_events (
    event_id     TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    resource_id  TEXT,
    payload      JSONB NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL,
    processed_at TIMESTAMPTZ,
    error        TEXT
);
