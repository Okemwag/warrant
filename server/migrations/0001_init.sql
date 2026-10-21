-- Permissions and their signed versions.
CREATE TABLE permissions (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('draft', 'active', 'paused', 'revoked')),
    current_version INT,              -- latest signed version; NULL until first signing
    latest_version  INT NOT NULL,     -- latest version, signed or not
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE permission_versions (
    permission_id TEXT NOT NULL REFERENCES permissions (id),
    version       INT NOT NULL,
    body          JSONB NOT NULL,     -- normalized engine.Permission
    canonical     BYTEA NOT NULL,     -- exact bytes that are hashed
    hash          BYTEA NOT NULL,     -- sha256(canonical)
    signature     BYTEA,              -- ed25519(hash); NULL while draft
    key_id        TEXT,
    signed_at     TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (permission_id, version)
);

-- One row per permission per calendar month (UTC). Every budget change locks
-- this row with SELECT ... FOR UPDATE.
CREATE TABLE budget_periods (
    permission_id   TEXT NOT NULL REFERENCES permissions (id),
    period          TEXT NOT NULL,    -- 'YYYY-MM'
    committed_cents BIGINT NOT NULL DEFAULT 0 CHECK (committed_cents >= 0),
    held_cents      BIGINT NOT NULL DEFAULT 0 CHECK (held_cents >= 0),
    PRIMARY KEY (permission_id, period)
);

CREATE TABLE decisions (
    id                 TEXT PRIMARY KEY,
    permission_id      TEXT NOT NULL REFERENCES permissions (id),
    permission_version INT,
    idempotency_key    TEXT,          -- NULL for BUDGET_CONFLICT, so a retry can succeed
    proposal           JSONB NOT NULL,
    outcome            TEXT NOT NULL CHECK (outcome IN ('ALLOW', 'CONFIRM', 'BLOCK')),
    reasons            TEXT[] NOT NULL,
    explanation        TEXT NOT NULL,
    amount_cents       BIGINT NOT NULL,
    remaining_cents    BIGINT NOT NULL,
    human_action       TEXT CHECK (human_action IN ('approved', 'declined')),
    human_at           TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL,
    UNIQUE (permission_id, idempotency_key)
);
CREATE INDEX decisions_permission_created ON decisions (permission_id, created_at DESC);

CREATE TABLE reservations (
    id             TEXT PRIMARY KEY,
    decision_id    TEXT NOT NULL UNIQUE REFERENCES decisions (id),
    permission_id  TEXT NOT NULL,
    period         TEXT NOT NULL,
    amount_cents   BIGINT NOT NULL CHECK (amount_cents > 0),
    state          TEXT NOT NULL CHECK (state IN ('held', 'committed', 'released', 'refunded')),
    release_reason TEXT,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (permission_id, period) REFERENCES budget_periods (permission_id, period)
);
CREATE INDEX reservations_held_expiry ON reservations (expires_at) WHERE state = 'held';
CREATE INDEX reservations_permission_held ON reservations (permission_id) WHERE state = 'held';

-- Activity timeline: everything that happened, in order.
CREATE TABLE events (
    id            BIGSERIAL PRIMARY KEY,
    permission_id TEXT NOT NULL,
    decision_id   TEXT,
    type          TEXT NOT NULL,
    data          JSONB NOT NULL DEFAULT '{}',
    at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX events_permission_at ON events (permission_id, at DESC);
