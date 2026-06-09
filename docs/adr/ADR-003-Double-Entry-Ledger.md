# ADR-003: Double Entry Ledger

## Context

The service must provide a reliable audit trail and support reconciliation of account balances. Transfers are financial in nature and require traceability for both source and destination movements.

## Decision

Implement a double-entry ledger model: each transfer creates exactly two ledger entries, one debit from the source wallet and one credit to the destination wallet.

## Alternatives Considered

- Update wallet balances directly without keeping separate ledger entries.
- Use a single ledger entry per transfer and infer the opposite side implicitly.
- Adopt full event sourcing for all balance changes.

## Consequences

- Benefits:
  - Enables a clear audit trail of every movement.
  - Makes reconciliation possible by re-computing balances from ledger entries.
  - Provides stronger accounting correctness and traceability.
- Tradeoffs:
  - Stores more records than a balance-only approach.
  - Introduces additional write overhead for every transfer.
  - Requires careful validation to keep debit/credit pairs consistent.
