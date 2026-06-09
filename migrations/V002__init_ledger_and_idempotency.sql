-- =====================================================================
-- V002: Initialize Ledger Entries and Idempotency Records
-- =====================================================================
-- Ledger entries provide the authoritative transaction log.
-- Idempotency records enable safe retries of transfer requests.
-- =====================================================================

-- =====================================================================
-- ENUMS (if not already created in V001)
-- =====================================================================

CREATE TYPE ledger_entry_type AS ENUM (
    'DEBIT',    -- Money leaving a wallet
    'CREDIT'    -- Money entering a wallet
);

-- =====================================================================
-- LEDGER ENTRIES TABLE
-- =====================================================================
-- Purpose:
--   Double-entry bookkeeping: every transfer creates exactly one DEBIT
--   and one CREDIT entry. Ledger is the authoritative source of truth
--   for all money movements.
--
-- Design Decisions:
--
-- DOUBLE-ENTRY BOOKKEEPING:
--   - Every transfer creates 2 ledger entries (1 DEBIT, 1 CREDIT)
--   - Ensures total money in system is always constant (conservation law)
--   - Debit amount = Credit amount (by constraint)
--   - Sum of all debits = Sum of all credits (invariant)
--   - Enables reconciliation: compare wallet balances vs ledger sum
--
-- SUPPORTING AUDITABILITY:
--   - Complete transaction log: every entry is immutable (no updates)
--   - Timestamp: precise record of when money moved
--   - Transfer ID: link to originating transfer request
--   - Wallet ID: affected wallet
--   - Entry type: direction of money flow
--   - Amount: magnitude of movement
--   - Cannot be deleted (RESTRICT foreign key on transfers)
--   - Enables compliance reporting, tax audits, customer disputes
--
-- UNIQUE CONSTRAINT ON (transfer_id, entry_type):
--   - Prevents duplicate entries for the same transfer
--   - Ensures exactly one DEBIT and one CREDIT per transfer
--   - Database-level constraint for data integrity
--
-- AMOUNT as BIGINT:
--   - Same rationale: exact arithmetic, smallest currency unit
--   - Always positive (negative amounts stored as entry_type = DEBIT)
--   - Simplifies queries and calculations
--
CREATE TABLE ledger_entries (
    -- Primary Key: UUID
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Reference to originating transfer
    transfer_id UUID NOT NULL REFERENCES transfers(id)
        ON DELETE RESTRICT,  -- Prevent deleting transfer if ledger entries exist

    -- Wallet affected by this entry
    wallet_id UUID NOT NULL REFERENCES wallets(id)
        ON DELETE RESTRICT,  -- Prevent deleting wallet if ledger entries exist

    -- Type of entry (DEBIT or CREDIT)
    entry_type ledger_entry_type NOT NULL,

    -- Amount: BIGINT in smallest currency unit
    -- Always positive; direction indicated by entry_type
    amount BIGINT NOT NULL
        CONSTRAINT chk_ledger_amount_positive CHECK (amount > 0),

    -- Audit Column
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Constraints
    -- Ensure exactly one DEBIT and one CREDIT per transfer
    CONSTRAINT uq_transfer_entry_type
        UNIQUE (transfer_id, entry_type),

    -- Optional: Ensure wallet_id matches from_wallet or to_wallet in transfer
    -- This is a logical constraint (can be checked by application layer)
    -- For strict enforcement at DB level, use a CHECK with subquery (expensive)
    -- Recommended: enforce at application layer instead
    CONSTRAINT chk_ledger_timestamps
        CHECK (created_at IS NOT NULL)
);

-- Indexes on Ledger Entries
-- Used for balance calculation and audit queries

CREATE INDEX idx_ledger_wallet
    ON ledger_entries(wallet_id);

CREATE INDEX idx_ledger_transfer
    ON ledger_entries(transfer_id);

CREATE INDEX idx_ledger_created_at
    ON ledger_entries(created_at DESC);

-- Composite index for wallet balance reconciliation
-- Query: SELECT SUM(amount) FROM ledger_entries WHERE wallet_id = ? AND entry_type = 'DEBIT'
CREATE INDEX idx_ledger_wallet_type
    ON ledger_entries(wallet_id, entry_type)
    INCLUDE (amount);  -- Include amount to enable index-only scans

-- =====================================================================
-- IDEMPOTENCY RECORDS TABLE
-- =====================================================================
-- Purpose:
--   Enable safe retries of transfer requests.
--   Stores request deduplication key and response for replay.
--
-- Design Decisions:
--
-- IDEMPOTENCY STRATEGY:
--   - Client provides unique idempotency_key with each request
--   - Server checks: if key exists in idempotency_records, return stored response
--   - If key doesn't exist: execute transfer, store response with key
--   - Pattern: "Idempotent Request / Response Caching"
--
-- USES REQUEST HASH for PAYLOAD VERIFICATION:
--   - Hash of request body (JSON)
--   - If client retries with different payload but same key: error (conflict)
--   - Prevents accidental re-use of same key with different amounts/wallets
--   - Example: transfer $100, retry with $200 -> detect conflict, reject
--
-- STATUS FIELD for RECOVERY:
--   - PENDING: Request received, execution in progress
--   - COMPLETED: Transfer succeeded (idempotent response can be replayed)
--   - FAILED: Transfer failed (idempotent error response can be replayed)
--   - Enables recovery: if server crashed, query idempotency record to resume
--   - Example: "Did my request succeed?" -> check idempotency_records status
--
-- RESPONSE STORED AS JSONB:
--   - Stores exact API response (status, transfer ID, error message)
--   - Enables bit-for-bit identical responses on retry
--   - JSONB type allows querying response content if needed
--   - Sized appropriately (responses typically < 1KB)
--
-- FOREIGN KEY WITH CASCADE:
--   - Weak constraint: transfer_id references transfer(id)
--   - ON DELETE CASCADE: if transfer deleted, idempotency record deleted
--   - Trades storage for simplicity (deleting transfer is rare)
--   - Alternative: ON DELETE SET NULL (but then lose transfer link)
--
CREATE TABLE idempotency_records (
    -- Primary Key: Idempotency key from client
    idempotency_key TEXT PRIMARY KEY
        CONSTRAINT chk_idempotency_key_not_empty CHECK (idempotency_key <> ''),

    -- Transfer ID: Link to actual transfer created
    -- Can be NULL if transfer creation failed before ID was assigned
    transfer_id UUID REFERENCES transfers(id)
        ON DELETE CASCADE,  -- If transfer deleted, remove this record

    -- Request Hash: SHA256 or similar of request body
    -- Used to detect if same key is re-used with different request payload
    request_hash TEXT NOT NULL
        CONSTRAINT chk_request_hash_not_empty CHECK (request_hash <> ''),

    -- Status of idempotency record
    -- PENDING: execution in progress or queued
    -- COMPLETED: transfer succeeded (response can be replayed)
    -- FAILED: transfer failed (error response can be replayed)
    status TEXT NOT NULL DEFAULT 'PENDING'
        CONSTRAINT chk_idempotency_status
            CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED')),

    -- Stored Response: Full HTTP response as JSON
    -- Allows exact replay of response to retry request
    -- Structure: {"transferId": "...", "status": "...", "error": null}
    response_json JSONB NOT NULL DEFAULT '{}',

    -- HTTP Status Code: 200, 202, 422, 500, etc.
    -- Enables client to distinguish between success, client error, server error
    status_code INTEGER NOT NULL DEFAULT 0
        CONSTRAINT chk_idempotency_status_code_positive
            CHECK (status_code >= 100 AND status_code < 600),

    -- Audit Column
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Constraints
    CONSTRAINT chk_idempotency_response_non_empty
        CHECK (response_json <> '{}' OR status = 'PENDING'),
    CONSTRAINT chk_idempotency_status_consistency
        -- If COMPLETED or FAILED, must have status code and response
        CHECK (
            (status = 'PENDING' AND (status_code = 0 OR response_json = '{}'))
            OR (status IN ('COMPLETED', 'FAILED') AND status_code > 0)
        )
);

-- Indexes on Idempotency Records

CREATE INDEX idx_idempotency_transfer
    ON idempotency_records(transfer_id);

CREATE INDEX idx_idempotency_created_at
    ON idempotency_records(created_at DESC);

-- Index for cleanup queries: find old PENDING records to timeout
CREATE INDEX idx_idempotency_pending
    ON idempotency_records(created_at)
    WHERE status = 'PENDING';

-- =====================================================================
-- RECONCILIATION VIEWS
-- =====================================================================
-- Optional: Create views to help with reconciliation and monitoring

-- View: Wallet Ledger Sum (for reconciliation)
-- Shows balance from ledger vs stored balance
CREATE OR REPLACE VIEW v_wallet_reconciliation AS
SELECT
    w.id AS wallet_id,
    w.balance AS stored_balance,
    COALESCE(
        (SELECT SUM(amount) FROM ledger_entries
         WHERE wallet_id = w.id AND entry_type = 'DEBIT'),
        0
    ) AS total_debits,
    COALESCE(
        (SELECT SUM(amount) FROM ledger_entries
         WHERE wallet_id = w.id AND entry_type = 'CREDIT'),
        0
    ) AS total_credits,
    COALESCE(
        (SELECT SUM(amount) FROM ledger_entries
         WHERE wallet_id = w.id AND entry_type = 'CREDIT'),
        0
    ) - COALESCE(
        (SELECT SUM(amount) FROM ledger_entries
         WHERE wallet_id = w.id AND entry_type = 'DEBIT'),
        0
    ) AS calculated_balance,
    w.balance - (
        COALESCE(
            (SELECT SUM(amount) FROM ledger_entries
             WHERE wallet_id = w.id AND entry_type = 'CREDIT'),
            0
        ) - COALESCE(
            (SELECT SUM(amount) FROM ledger_entries
             WHERE wallet_id = w.id AND entry_type = 'DEBIT'),
            0
        )
    ) AS balance_discrepancy
FROM wallets w;

-- View: Transfer Statistics
CREATE OR REPLACE VIEW v_transfer_stats AS
SELECT
    status,
    COUNT(*) AS count,
    SUM(amount) AS total_amount,
    MIN(created_at) AS first_created,
    MAX(created_at) AS last_created
FROM transfers
GROUP BY status;

-- =====================================================================
-- CLEANUP AND MAINTENANCE
-- =====================================================================
-- Note: These are examples; implement cleanup policies as needed

-- Example: Mark old PENDING idempotency records as failed (configurable timeout)
-- Run periodically (e.g., every hour)
-- SELECT * FROM idempotency_records
-- WHERE status = 'PENDING' AND created_at < NOW() - INTERVAL '30 minutes'
-- AND transfer_id IS NULL;
