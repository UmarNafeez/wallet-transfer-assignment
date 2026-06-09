# ADR-005: Idempotency Design

## Context

The transfer API is exposed to clients that may retry requests due to network timeouts or uncertain responses. The system must avoid duplicate transfers and preserve exactly-once semantics at the API level.

## Decision

Implement idempotency using a persistent idempotency record store keyed by client-provided idempotency keys. Persist request metadata and outcome so repeated requests with the same key return the original result instead of creating duplicate transfers.

## Alternatives Considered

- Rely only on client-generated transfer IDs as the unique key.
- Use in-memory deduplication with a TTL.
- Use a message queue with deduplication at the queue layer.
- Accept eventual duplicate processing and rely on compensating actions.

## Consequences

- Benefits:
  - Provides stronger idempotent behavior across retries and restarts.
  - Supports replay-safe API semantics and predictable duplicate handling.
  - Avoids duplicate ledger entries and duplicate balance changes.
- Tradeoffs:
  - Requires persistent storage for idempotency records.
  - Introduces additional write overhead for transfer requests.
  - Requires careful handling of record uniqueness, expiry, and cleanup policy.
