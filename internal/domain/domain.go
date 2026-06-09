package domain

import "errors"

var (
	ErrEmptyWalletID             = errors.New("wallet ID is required")
	ErrNegativeBalance           = errors.New("wallet balance must be non-negative")
	ErrInvalidAmount             = errors.New("amount must be greater than zero")
	ErrInsufficientFunds         = errors.New("insufficient funds")
	ErrEmptyTransferID           = errors.New("transfer ID is required")
	ErrEmptyIdempotencyKey       = errors.New("idempotency key is required")
	ErrSameWalletIDs             = errors.New("from and to wallet IDs must differ")
	ErrInvalidWalletID           = errors.New("from and to wallet IDs are required")
	ErrInvalidTransferStatus     = errors.New("invalid transfer status")
	ErrInvalidStateTransition    = errors.New("invalid state transition")
	ErrInvalidLedgerEntryType    = errors.New("invalid ledger entry type")
	ErrInvalidLedgerEntries      = errors.New("invalid ledger entries")
	ErrLedgerEntrySameWalletID   = errors.New("ledger entries must reference different wallets")
	ErrLedgerEntryAmountMismatch = errors.New("ledger entries must have matching amounts")
)

// TransferStatus represents the lifecycle status of a transfer.
type TransferStatus string

const (
	TransferStatusPending   TransferStatus = "PENDING"
	TransferStatusProcessed TransferStatus = "PROCESSED"
	TransferStatusFailed    TransferStatus = "FAILED"
)

func (s TransferStatus) IsValid() bool {
	switch s {
	case TransferStatusPending, TransferStatusProcessed, TransferStatusFailed:
		return true
	default:
		return false
	}
}

func (s TransferStatus) CanTransitionTo(next TransferStatus) bool {
	if !s.IsValid() || !next.IsValid() {
		return false
	}
	if s == next {
		return true
	}

	switch s {
	case TransferStatusPending:
		return next == TransferStatusProcessed || next == TransferStatusFailed
	default:
		return false
	}
}

// LedgerEntryType represents whether a ledger entry is a debit or credit.
type LedgerEntryType string

const (
	LedgerEntryTypeDebit  LedgerEntryType = "DEBIT"
	LedgerEntryTypeCredit LedgerEntryType = "CREDIT"
)

func (t LedgerEntryType) IsValid() bool {
	switch t {
	case LedgerEntryTypeDebit, LedgerEntryTypeCredit:
		return true
	default:
		return false
	}
}

// Wallet models a wallet with a current balance and optional optimistic version.
type Wallet struct {
	ID      string
	Balance int64
	Version int64
}

func NewWallet(id string, balance int64) (*Wallet, error) {
	wallet := &Wallet{ID: id, Balance: balance}
	if err := wallet.Validate(); err != nil {
		return nil, err
	}
	return wallet, nil
}

func (w *Wallet) Validate() error {
	if w.ID == "" {
		return ErrEmptyWalletID
	}
	if w.Balance < 0 {
		return ErrNegativeBalance
	}
	return nil
}

func (w *Wallet) CanDebit(amount int64) bool {
	return amount > 0 && w.Balance >= amount
}

func (w *Wallet) Debit(amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if amount > w.Balance {
		return ErrInsufficientFunds
	}
	w.Balance -= amount
	return nil
}

func (w *Wallet) Credit(amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	w.Balance += amount
	return nil
}

// Transfer models a wallet-to-wallet transfer request and its lifecycle.
type Transfer struct {
	ID             string
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
	Status         TransferStatus
	FailureReason  string
}

func NewTransfer(id, idempotencyKey, fromWalletID, toWalletID string, amount int64) (*Transfer, error) {
	transfer := &Transfer{
		ID:             id,
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         amount,
		Status:         TransferStatusPending,
	}

	if err := transfer.Validate(); err != nil {
		return nil, err
	}

	return transfer, nil
}

func (t *Transfer) Validate() error {
	if t.ID == "" {
		return ErrEmptyTransferID
	}
	if t.IdempotencyKey == "" {
		return ErrEmptyIdempotencyKey
	}
	if t.FromWalletID == "" || t.ToWalletID == "" {
		return ErrInvalidWalletID
	}
	if t.FromWalletID == t.ToWalletID {
		return ErrSameWalletIDs
	}
	if t.Amount <= 0 {
		return ErrInvalidAmount
	}
	if !t.Status.IsValid() {
		return ErrInvalidTransferStatus
	}
	return nil
}

func (t *Transfer) CanTransitionTo(next TransferStatus) bool {
	return t.Status.CanTransitionTo(next)
}

func (t *Transfer) TransitionTo(next TransferStatus, failureReason string) error {
	if !t.CanTransitionTo(next) {
		return ErrInvalidStateTransition
	}

	t.Status = next
	if next == TransferStatusFailed {
		t.FailureReason = failureReason
	} else if next == TransferStatusProcessed {
		t.FailureReason = ""
	}
	return nil
}

// LedgerEntry represents one side of a double-entry transfer.
type LedgerEntry struct {
	ID         string
	TransferID string
	WalletID   string
	EntryType  LedgerEntryType
	Amount     int64
}

func NewLedgerEntry(id, transferID, walletID string, entryType LedgerEntryType, amount int64) (*LedgerEntry, error) {
	entry := &LedgerEntry{
		ID:         id,
		TransferID: transferID,
		WalletID:   walletID,
		EntryType:  entryType,
		Amount:     amount,
	}
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	return entry, nil
}

func (l *LedgerEntry) Validate() error {
	if l.ID == "" {
		return errors.New("ledger entry ID is required")
	}
	if l.TransferID == "" {
		return errors.New("ledger transfer ID is required")
	}
	if l.WalletID == "" {
		return errors.New("ledger wallet ID is required")
	}
	if !l.EntryType.IsValid() {
		return ErrInvalidLedgerEntryType
	}
	if l.Amount <= 0 {
		return ErrInvalidAmount
	}
	return nil
}

func ValidateLedgerEntries(entries []LedgerEntry) error {
	if len(entries) != 2 {
		return ErrInvalidLedgerEntries
	}

	left, right := entries[0], entries[1]
	if left.TransferID == "" || right.TransferID == "" || left.TransferID != right.TransferID {
		return ErrInvalidLedgerEntries
	}
	if left.WalletID == "" || right.WalletID == "" {
		return ErrInvalidLedgerEntries
	}
	if left.WalletID == right.WalletID {
		return ErrLedgerEntrySameWalletID
	}
	if !left.EntryType.IsValid() || !right.EntryType.IsValid() {
		return ErrInvalidLedgerEntryType
	}
	if left.Amount <= 0 || right.Amount <= 0 {
		return ErrInvalidAmount
	}
	if left.Amount != right.Amount {
		return ErrLedgerEntryAmountMismatch
	}
	if left.EntryType == right.EntryType {
		return ErrInvalidLedgerEntries
	}
	if (left.EntryType == LedgerEntryTypeDebit && right.EntryType != LedgerEntryTypeCredit) ||
		(left.EntryType == LedgerEntryTypeCredit && right.EntryType != LedgerEntryTypeDebit) {
		return ErrInvalidLedgerEntries
	}
	return nil
}
