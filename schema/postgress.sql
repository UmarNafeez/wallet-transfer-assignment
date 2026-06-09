-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TYPE transfer_status AS ENUM (
    'PENDING',
    'PROCESSED',
    'FAILED'
);

CREATE TYPE ledger_entry_type AS ENUM (
    'DEBIT',
    'CREDIT'
);

CREATE TABLE wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    balance BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_wallet_balance_non_negative
        CHECK (balance >= 0)
);

CREATE TABLE transfers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    idempotency_key TEXT NOT NULL UNIQUE,

    from_wallet_id UUID NOT NULL REFERENCES wallets(id),
    to_wallet_id UUID NOT NULL REFERENCES wallets(id),

    amount BIGINT NOT NULL,

    status transfer_status NOT NULL,

    failure_reason TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_transfer_amount_positive
        CHECK (amount > 0),

    CONSTRAINT chk_transfer_different_wallets
        CHECK (from_wallet_id <> to_wallet_id)
);

CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    transfer_id UUID NOT NULL REFERENCES transfers(id),
    wallet_id UUID NOT NULL REFERENCES wallets(id),

    entry_type ledger_entry_type NOT NULL,
    amount BIGINT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_ledger_amount_positive
        CHECK (amount > 0),

    CONSTRAINT uq_transfer_entry_type
        UNIQUE (transfer_id, entry_type)
);

CREATE INDEX idx_ledger_wallet
    ON ledger_entries(wallet_id);

CREATE INDEX idx_transfer_from_wallet
    ON transfers(from_wallet_id);

CREATE INDEX idx_transfer_to_wallet
    ON transfers(to_wallet_id);

-- ==========================================
-- IDEMPOTENCY RECORDS
-- ==========================================
-- Dedicated table for request deduplication and response replay

CREATE TABLE idempotency_records (
    idempotency_key TEXT PRIMARY KEY,

    transfer_id UUID NOT NULL,

    request_hash TEXT NOT NULL,

    status TEXT NOT NULL,

    response_json JSONB NOT NULL,

    status_code INTEGER NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_idempotency_status
        CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED')),

    CONSTRAINT fk_idempotency_transfer
        FOREIGN KEY (transfer_id)
        REFERENCES transfers(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_idempotency_transfer
    ON idempotency_records(transfer_id);
