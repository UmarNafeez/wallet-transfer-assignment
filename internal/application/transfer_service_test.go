package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	repository "github.com/Robustrade/wallet-transfer-assignment/internal/respository"
)

var errTest = errors.New("test error")

func TestTransferService_Execute_Success(t *testing.T) {
	var called []string

	walletRepo := &mockWalletRepo{
		getByIDForUpdateFn: func(ctx context.Context, walletID string) (*domain.Wallet, error) {
			called = append(called, "wallet-lock:"+walletID)
			return &domain.Wallet{ID: walletID, Balance: 100}, nil
		},
		updateFn: func(ctx context.Context, wallet *domain.Wallet) error {
			called = append(called, "wallet-update:"+wallet.ID)
			return nil
		},
	}

	transferRepo := &mockTransferRepo{
		getByIdempotencyKeyFn: func(ctx context.Context, key string) (*domain.Transfer, error) {
			return nil, repository.ErrNotFound
		},
		createFn: func(ctx context.Context, transfer *domain.Transfer) error {
			called = append(called, "transfer-create")
			return nil
		},
		updateStatusFn: func(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
			called = append(called, "transfer-status:"+string(status))
			return nil
		},
	}

	ledgerRepo := &mockLedgerRepo{
		createEntriesFn: func(ctx context.Context, entries []domain.LedgerEntry) error {
			if len(entries) != 2 {
				return errors.New("expected 2 ledger entries")
			}
			if entries[0].Amount != entries[1].Amount {
				return errors.New("amount mismatch")
			}
			called = append(called, "ledger-create")
			return nil
		},
	}

	idemRepo := &mockIdempotencyRepo{
		getByKeyFn: func(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
			called = append(called, "idempotency-get")
			return nil, repository.ErrNotFound
		},
		createFn: func(ctx context.Context, record *repository.IdempotencyRecord) error {
			called = append(called, "idempotency-create")
			return nil
		},
		updateResponseFn: func(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error {
			called = append(called, "idempotency-update")
			return nil
		},
	}

	service := NewTransferService(&mockTransactionManager{}, walletRepo, transferRepo, ledgerRepo, idemRepo, nil)

	req := TransferRequest{
		TransferID:     "transfer-1",
		IdempotencyKey: "idem-1",
		FromWalletID:   "wallet-a",
		ToWalletID:     "wallet-b",
		Amount:         100,
		RequestHash:    "hash-1",
	}

	transfer, err := service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if transfer.Status != domain.TransferStatusProcessed {
		t.Fatalf("expected transfer status processed, got %s", transfer.Status)
	}

	expectedCalls := []string{
		"idempotency-get",
		"idempotency-create",
		"transfer-create",
		"wallet-lock:wallet-a",
		"wallet-lock:wallet-b",
		"wallet-update:wallet-a",
		"wallet-update:wallet-b",
		"ledger-create",
		"transfer-status:PROCESSED",
		"idempotency-update",
	}

	for i, name := range expectedCalls {
		if i >= len(called) || called[i] != name {
			t.Fatalf("expected call %d %s, got %v", i, name, called)
		}
	}
}

func TestTransferService_Execute_IdempotentDuplicateReturnsExistingTransfer(t *testing.T) {
	transferRepo := &mockTransferRepo{
		getByIdempotencyKeyFn: func(ctx context.Context, key string) (*domain.Transfer, error) {
			return &domain.Transfer{ID: "transfer-2", Status: domain.TransferStatusProcessed}, nil
		},
		getByIDFn: func(ctx context.Context, transferID string) (*domain.Transfer, error) {
			return &domain.Transfer{ID: transferID, Status: domain.TransferStatusProcessed}, nil
		},
	}
	idemRepo := &mockIdempotencyRepo{
		getByKeyFn: func(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
			return &repository.IdempotencyRecord{IdempotencyKey: key, TransferID: "transfer-2", RequestHash: "hash-1"}, nil
		},
	}

	service := NewTransferService(&mockTransactionManager{}, &mockWalletRepo{}, transferRepo, &mockLedgerRepo{}, idemRepo, nil)

	req := TransferRequest{TransferID: "transfer-2", IdempotencyKey: "idem-2", RequestHash: "hash-1"}
	transfer, err := service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if transfer.ID != "transfer-2" {
		t.Fatalf("expected existing transfer, got %s", transfer.ID)
	}
}

func TestTransferService_Execute_IdempotencyHashMismatch(t *testing.T) {
	transferRepo := &mockTransferRepo{}
	idemRepo := &mockIdempotencyRepo{
		getByKeyFn: func(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
			return &repository.IdempotencyRecord{IdempotencyKey: key, TransferID: "transfer-2", RequestHash: "hash-1"}, nil
		},
	}
	service := NewTransferService(&mockTransactionManager{}, &mockWalletRepo{}, transferRepo, &mockLedgerRepo{}, idemRepo, nil)

	req := TransferRequest{TransferID: "transfer-2", IdempotencyKey: "idem-2", RequestHash: "hash-2"}
	_, err := service.Execute(context.Background(), req)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestTransferService_Execute_InsufficientFundsMarksTransferFailed(t *testing.T) {
	walletRepo := &mockWalletRepo{
		getByIDForUpdateFn: func(ctx context.Context, walletID string) (*domain.Wallet, error) {
			return &domain.Wallet{ID: walletID, Balance: 50}, nil
		},
	}
	transferRepo := &mockTransferRepo{
		getByIdempotencyKeyFn: func(ctx context.Context, key string) (*domain.Transfer, error) {
			return nil, repository.ErrNotFound
		},
		createFn: func(ctx context.Context, transfer *domain.Transfer) error {
			return nil
		},
		updateStatusFn: func(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
			if status != domain.TransferStatusFailed {
				t.Fatal("expected failed status")
			}
			if failureReason == "" {
				t.Fatal("expected failure reason")
			}
			return nil
		},
	}
	idemRepo := &mockIdempotencyRepo{
		getByKeyFn: func(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
			return nil, repository.ErrNotFound
		},
		createFn: func(ctx context.Context, record *repository.IdempotencyRecord) error {
			return nil
		},
	}
	service := NewTransferService(&mockTransactionManager{}, walletRepo, transferRepo, &mockLedgerRepo{}, idemRepo, nil)

	req := TransferRequest{TransferID: "transfer-4", IdempotencyKey: "idem-4", FromWalletID: "wallet-a", ToWalletID: "wallet-b", Amount: 100, RequestHash: "hash-4"}
	_, err := service.Execute(context.Background(), req)
	if !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds error, got %v", err)
	}
}

func TestTransferService_Execute_DeterministicLockOrder(t *testing.T) {
	var order []string
	walletRepo := &mockWalletRepo{
		getByIDForUpdateFn: func(ctx context.Context, walletID string) (*domain.Wallet, error) {
			order = append(order, walletID)
			return &domain.Wallet{ID: walletID, Balance: 100}, nil
		},
		updateFn: func(ctx context.Context, wallet *domain.Wallet) error {
			return nil
		},
	}
	transferRepo := &mockTransferRepo{
		getByIdempotencyKeyFn: func(ctx context.Context, key string) (*domain.Transfer, error) {
			return nil, repository.ErrNotFound
		},
		createFn: func(ctx context.Context, transfer *domain.Transfer) error { return nil },
		updateStatusFn: func(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
			return nil
		},
	}
	ledgerRepo := &mockLedgerRepo{createEntriesFn: func(ctx context.Context, entries []domain.LedgerEntry) error { return nil }}
	idemRepo := &mockIdempotencyRepo{getByKeyFn: func(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
		return nil, repository.ErrNotFound
	}, createFn: func(ctx context.Context, record *repository.IdempotencyRecord) error { return nil }, updateResponseFn: func(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error {
		return nil
	}}

	service := NewTransferService(&mockTransactionManager{}, walletRepo, transferRepo, ledgerRepo, idemRepo, nil)

	req := TransferRequest{TransferID: "transfer-5", IdempotencyKey: "idem-5", FromWalletID: "wallet-b", ToWalletID: "wallet-a", Amount: 10, RequestHash: "hash-5"}
	_, err := service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if len(order) != 2 || order[0] != "wallet-a" || order[1] != "wallet-b" {
		t.Fatalf("expected deterministic lock order [wallet-a,wallet-b], got %v", order)
	}
}

// Mock implementations

type mockTransactionManager struct{}

func (m *mockTransactionManager) RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type mockWalletRepo struct {
	createFn           func(ctx context.Context, wallet *domain.Wallet) error
	getByIDFn          func(ctx context.Context, walletID string) (*domain.Wallet, error)
	getByIDForUpdateFn func(ctx context.Context, walletID string) (*domain.Wallet, error)
	updateFn           func(ctx context.Context, wallet *domain.Wallet) error
}

func (m *mockWalletRepo) Create(ctx context.Context, wallet *domain.Wallet) error {
	if m.createFn != nil {
		return m.createFn(ctx, wallet)
	}
	return nil
}
func (m *mockWalletRepo) GetByID(ctx context.Context, walletID string) (*domain.Wallet, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, walletID)
	}
	return nil, repository.ErrNotFound
}
func (m *mockWalletRepo) GetByIDForUpdate(ctx context.Context, walletID string) (*domain.Wallet, error) {
	if m.getByIDForUpdateFn != nil {
		return m.getByIDForUpdateFn(ctx, walletID)
	}
	return nil, repository.ErrNotFound
}
func (m *mockWalletRepo) Update(ctx context.Context, wallet *domain.Wallet) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, wallet)
	}
	return nil
}

type mockTransferRepo struct {
	createFn              func(ctx context.Context, transfer *domain.Transfer) error
	getByIDFn             func(ctx context.Context, transferID string) (*domain.Transfer, error)
	getByIdempotencyKeyFn func(ctx context.Context, key string) (*domain.Transfer, error)
	updateStatusFn        func(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error
}

func (m *mockTransferRepo) Create(ctx context.Context, transfer *domain.Transfer) error {
	if m.createFn != nil {
		return m.createFn(ctx, transfer)
	}
	return nil
}
func (m *mockTransferRepo) GetByID(ctx context.Context, transferID string) (*domain.Transfer, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, transferID)
	}
	return nil, repository.ErrNotFound
}
func (m *mockTransferRepo) GetByIdempotencyKey(ctx context.Context, key string) (*domain.Transfer, error) {
	if m.getByIdempotencyKeyFn != nil {
		return m.getByIdempotencyKeyFn(ctx, key)
	}
	return nil, repository.ErrNotFound
}
func (m *mockTransferRepo) UpdateStatus(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, transferID, status, failureReason)
	}
	return nil
}

type mockLedgerRepo struct {
	createEntriesFn   func(ctx context.Context, entries []domain.LedgerEntry) error
	getByTransferIDFn func(ctx context.Context, transferID string) ([]domain.LedgerEntry, error)
	getByWalletIDFn   func(ctx context.Context, walletID string) ([]domain.LedgerEntry, error)
}

func (m *mockLedgerRepo) CreateEntries(ctx context.Context, entries []domain.LedgerEntry) error {
	if m.createEntriesFn != nil {
		return m.createEntriesFn(ctx, entries)
	}
	return nil
}
func (m *mockLedgerRepo) GetByTransferID(ctx context.Context, transferID string) ([]domain.LedgerEntry, error) {
	if m.getByTransferIDFn != nil {
		return m.getByTransferIDFn(ctx, transferID)
	}
	return nil, repository.ErrNotFound
}
func (m *mockLedgerRepo) GetByWalletID(ctx context.Context, walletID string) ([]domain.LedgerEntry, error) {
	if m.getByWalletIDFn != nil {
		return m.getByWalletIDFn(ctx, walletID)
	}
	return nil, repository.ErrNotFound
}

type mockIdempotencyRepo struct {
	createFn         func(ctx context.Context, record *repository.IdempotencyRecord) error
	getByKeyFn       func(ctx context.Context, key string) (*repository.IdempotencyRecord, error)
	updateResponseFn func(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error
	updateStatusFn   func(ctx context.Context, key string, status repository.IdempotencyStatus) error
}

func (m *mockIdempotencyRepo) Create(ctx context.Context, record *repository.IdempotencyRecord) error {
	if m.createFn != nil {
		return m.createFn(ctx, record)
	}
	return nil
}
func (m *mockIdempotencyRepo) GetByKey(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
	if m.getByKeyFn != nil {
		return m.getByKeyFn(ctx, key)
	}
	return nil, repository.ErrNotFound
}
func (m *mockIdempotencyRepo) UpdateResponse(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error {
	if m.updateResponseFn != nil {
		return m.updateResponseFn(ctx, key, responseJSON, statusCode, status)
	}
	return nil
}
func (m *mockIdempotencyRepo) UpdateStatus(ctx context.Context, key string, status repository.IdempotencyStatus) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, key, status)
	}
	return nil
}

// concurrency helpers

type ctxTxKey struct{}

type concurrentTransactionManager struct {
	store *concurrentStore
}

func (m *concurrentTransactionManager) RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	tx := &fakeTx{store: m.store, locked: make(map[string]struct{})}
	ctx = context.WithValue(ctx, ctxTxKey{}, tx)
	err := fn(ctx)
	tx.release()
	return err
}

type fakeTx struct {
	store  *concurrentStore
	locked map[string]struct{}
}

func (tx *fakeTx) lockWallet(walletID string) {
	if _, ok := tx.locked[walletID]; ok {
		return
	}
	lock := tx.store.getWalletLock(walletID)
	lock.Lock()
	tx.locked[walletID] = struct{}{}
}

func (tx *fakeTx) release() {
	for walletID := range tx.locked {
		tx.store.getWalletLock(walletID).Unlock()
	}
}

func getTx(ctx context.Context) (*fakeTx, error) {
	tx, ok := ctx.Value(ctxTxKey{}).(*fakeTx)
	if !ok || tx == nil {
		return nil, errors.New("transaction required")
	}
	return tx, nil
}

type concurrentStore struct {
	mu                    sync.RWMutex
	wallets               map[string]*domain.Wallet
	transfers             map[string]*domain.Transfer
	transferByIdempotency map[string]*domain.Transfer
	idemRecords           map[string]*repository.IdempotencyRecord
	ledger                []domain.LedgerEntry
	walletLocks           map[string]*sync.Mutex
	walletLocksMu         sync.Mutex
}

func newConcurrentStore() *concurrentStore {
	return &concurrentStore{
		wallets:               make(map[string]*domain.Wallet),
		transfers:             make(map[string]*domain.Transfer),
		transferByIdempotency: make(map[string]*domain.Transfer),
		idemRecords:           make(map[string]*repository.IdempotencyRecord),
		ledger:                make([]domain.LedgerEntry, 0),
		walletLocks:           make(map[string]*sync.Mutex),
	}
}

func (s *concurrentStore) getWalletLock(id string) *sync.Mutex {
	s.walletLocksMu.Lock()
	defer s.walletLocksMu.Unlock()
	lock, ok := s.walletLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		s.walletLocks[id] = lock
	}
	return lock
}

func (s *concurrentStore) createWallet(id string, balance int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallets[id] = &domain.Wallet{ID: id, Balance: balance}
}

func (s *concurrentStore) GetWallet(id string) (*domain.Wallet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wallet, ok := s.wallets[id]
	if !ok {
		return nil, false
	}
	copy := *wallet
	return &copy, true
}

func (s *concurrentStore) GetAllWallets() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]int64, len(s.wallets))
	for k, v := range s.wallets {
		result[k] = v.Balance
	}
	return result
}

func (s *concurrentStore) getOrCopyTransfer(id string) (*domain.Transfer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	transfer, ok := s.transfers[id]
	if !ok {
		return nil, false
	}
	copy := *transfer
	return &copy, true
}

func (s *concurrentStore) walletRepoGetByIDForUpdate(ctx context.Context, walletID string) (*domain.Wallet, error) {
	tx, err := getTx(ctx)
	if err != nil {
		return nil, err
	}
	tx.lockWallet(walletID)

	s.mu.RLock()
	wallet, ok := s.wallets[walletID]
	s.mu.RUnlock()
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *wallet
	return &copy, nil
}

func (s *concurrentStore) walletRepoUpdate(ctx context.Context, wallet *domain.Wallet) error {
	tx, err := getTx(ctx)
	if err != nil {
		return err
	}
	if _, ok := tx.locked[wallet.ID]; !ok {
		return errors.New("wallet must be locked for update")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.wallets[wallet.ID]
	if !ok {
		return repository.ErrNotFound
	}
	stored.Balance = wallet.Balance
	stored.Version++
	return nil
}

func (s *concurrentStore) walletRepoCreate(ctx context.Context, wallet *domain.Wallet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.wallets[wallet.ID]; ok {
		return repository.ErrConflict
	}
	copy := *wallet
	s.wallets[wallet.ID] = &copy
	return nil
}

func (s *concurrentStore) transferRepoCreate(ctx context.Context, transfer *domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transfers[transfer.ID]; ok {
		return repository.ErrConflict
	}
	if _, ok := s.transferByIdempotency[transfer.IdempotencyKey]; ok {
		return repository.ErrConflict
	}
	copy := *transfer
	s.transfers[transfer.ID] = &copy
	s.transferByIdempotency[copy.IdempotencyKey] = &copy
	return nil
}

func (s *concurrentStore) transferRepoGetByID(ctx context.Context, transferID string) (*domain.Transfer, error) {
	transfer, ok := s.getOrCopyTransfer(transferID)
	if !ok {
		return nil, repository.ErrNotFound
	}
	return transfer, nil
}

func (s *concurrentStore) transferRepoGetByIdempotencyKey(ctx context.Context, key string) (*domain.Transfer, error) {
	s.mu.RLock()
	transfer, ok := s.transferByIdempotency[key]
	s.mu.RUnlock()
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *transfer
	return &copy, nil
}

func (s *concurrentStore) transferRepoUpdateStatus(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	transfer, ok := s.transfers[transferID]
	if !ok {
		return repository.ErrNotFound
	}
	transfer.Status = status
	transfer.FailureReason = failureReason
	return nil
}

func (s *concurrentStore) ledgerRepoCreateEntries(ctx context.Context, entries []domain.LedgerEntry) error {
	if len(entries) != 2 {
		return errors.New("ledger entries must contain exactly two entries")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range entries {
		for _, existing := range s.ledger {
			if existing.ID == entry.ID {
				return repository.ErrConflict
			}
		}
		s.ledger = append(s.ledger, entry)
	}
	return nil
}

func (s *concurrentStore) ledgerRepoGetByTransferID(ctx context.Context, transferID string) ([]domain.LedgerEntry, error) {
	response := make([]domain.LedgerEntry, 0)
	s.mu.RLock()
	for _, entry := range s.ledger {
		if entry.TransferID == transferID {
			response = append(response, entry)
		}
	}
	s.mu.RUnlock()
	if len(response) == 0 {
		return nil, repository.ErrNotFound
	}
	return response, nil
}

func (s *concurrentStore) ledgerRepoGetByWalletID(ctx context.Context, walletID string) ([]domain.LedgerEntry, error) {
	response := make([]domain.LedgerEntry, 0)
	s.mu.RLock()
	for _, entry := range s.ledger {
		if entry.WalletID == walletID {
			response = append(response, entry)
		}
	}
	s.mu.RUnlock()
	if len(response) == 0 {
		return nil, repository.ErrNotFound
	}
	return response, nil
}

func (s *concurrentStore) idempotencyRepoCreate(ctx context.Context, record *repository.IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idemRecords[record.IdempotencyKey]; ok {
		return repository.ErrConflict
	}
	copy := *record
	s.idemRecords[record.IdempotencyKey] = &copy
	return nil
}

func (s *concurrentStore) idempotencyRepoGetByKey(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
	s.mu.RLock()
	record, ok := s.idemRecords[key]
	s.mu.RUnlock()
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *record
	return &copy, nil
}

func (s *concurrentStore) idempotencyRepoUpdateResponse(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idemRecords[key]
	if !ok {
		return repository.ErrNotFound
	}
	record.ResponseJSON = append([]byte(nil), responseJSON...)
	record.StatusCode = statusCode
	record.Status = status
	return nil
}

func (s *concurrentStore) idempotencyRepoUpdateStatus(ctx context.Context, key string, status repository.IdempotencyStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.idemRecords[key]
	if !ok {
		return repository.ErrNotFound
	}
	record.Status = status
	return nil
}

func (s *concurrentStore) totalBalance() int64 {
	var sum int64
	s.mu.RLock()
	for _, wallet := range s.wallets {
		sum += wallet.Balance
	}
	s.mu.RUnlock()
	return sum
}

func (s *concurrentStore) ledgerCount() int {
	s.mu.RLock()
	count := len(s.ledger)
	s.mu.RUnlock()
	return count
}

func (s *concurrentStore) transferCount() int {
	s.mu.RLock()
	count := len(s.transfers)
	s.mu.RUnlock()
	return count
}

func (s *concurrentStore) idempotencyCount() int {
	s.mu.RLock()
	count := len(s.idemRecords)
	s.mu.RUnlock()
	return count
}

func newConcurrentService(store *concurrentStore) *TransferService {
	return NewTransferService(
		&concurrentTransactionManager{store},
		&walletRepoAdapter{store},
		&transferRepoAdapter{store},
		&ledgerRepoAdapter{store},
		&idempotencyRepoAdapter{store},
		nil,
	)
}

func TestTransferService_Execute_ConcurrentDebitsSameWallet(t *testing.T) {
	store := newConcurrentStore()
	store.createWallet("wallet-a", 1000)
	for i := 0; i < 100; i++ {
		store.createWallet(fmt.Sprintf("wallet-dest-%d", i), 0)
	}

	service := newConcurrentService(store)
	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	for i := 0; i < 100; i++ {
		req := TransferRequest{
			TransferID:     fmt.Sprintf("tx-%d", i),
			IdempotencyKey: fmt.Sprintf("idem-%d", i),
			FromWalletID:   "wallet-a",
			ToWalletID:     fmt.Sprintf("wallet-dest-%d", i),
			Amount:         10,
			RequestHash:    fmt.Sprintf("hash-%d", i),
		}
		wg.Add(1)
		go func(req TransferRequest) {
			defer wg.Done()
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}(req)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	}

	walletA, ok := store.GetWallet("wallet-a")
	if !ok {
		t.Fatal("wallet-a missing")
	}
	if walletA.Balance != 0 {
		t.Fatalf("expected wallet-a balance 0, got %d", walletA.Balance)
	}
	if store.transferCount() != 100 {
		t.Fatalf("expected 100 transfers, got %d", store.transferCount())
	}
	if store.ledgerCount() != 200 {
		t.Fatalf("expected 200 ledger entries, got %d", store.ledgerCount())
	}
}

func TestTransferService_Execute_ConcurrentDuplicateIdempotencyKeys(t *testing.T) {
	store := newConcurrentStore()
	store.createWallet("wallet-a", 1000)
	store.createWallet("wallet-b", 0)

	service := newConcurrentService(store)
	var wg sync.WaitGroup
	errCh := make(chan error, 50)

	req := TransferRequest{
		TransferID:     "shared-tx",
		IdempotencyKey: "shared-idem",
		FromWalletID:   "wallet-a",
		ToWalletID:     "wallet-b",
		Amount:         100,
		RequestHash:    "shared-hash",
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("expected no error for duplicate idempotency requests, got %v", err)
		}
	}

	if store.transferCount() != 1 {
		t.Fatalf("expected 1 transfer, got %d", store.transferCount())
	}
	if store.idempotencyCount() != 1 {
		t.Fatalf("expected 1 idempotency record, got %d", store.idempotencyCount())
	}
	if store.ledgerCount() != 2 {
		t.Fatalf("expected 2 ledger entries, got %d", store.ledgerCount())
	}
}

func TestTransferService_Execute_ConcurrentOppositeTransfersNoDeadlock(t *testing.T) {
	store := newConcurrentStore()
	store.createWallet("wallet-a", 500)
	store.createWallet("wallet-b", 500)

	service := newConcurrentService(store)
	var wg sync.WaitGroup
	errCh := make(chan error, 40)

	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			req := TransferRequest{
				TransferID:     fmt.Sprintf("a2b-%d", i),
				IdempotencyKey: fmt.Sprintf("a2b-idem-%d", i),
				FromWalletID:   "wallet-a",
				ToWalletID:     "wallet-b",
				Amount:         10,
				RequestHash:    fmt.Sprintf("hash-a2b-%d", i),
			}
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}(i)

		go func(i int) {
			defer wg.Done()
			req := TransferRequest{
				TransferID:     fmt.Sprintf("b2a-%d", i),
				IdempotencyKey: fmt.Sprintf("b2a-idem-%d", i),
				FromWalletID:   "wallet-b",
				ToWalletID:     "wallet-a",
				Amount:         10,
				RequestHash:    fmt.Sprintf("hash-b2a-%d", i),
			}
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("expected no error for opposite transfers, got %v", err)
		}
	}

	walletA, _ := store.GetWallet("wallet-a")
	walletB, _ := store.GetWallet("wallet-b")
	if walletA.Balance != 500 || walletB.Balance != 500 {
		t.Fatalf("expected balances to return to 500, got a=%d b=%d", walletA.Balance, walletB.Balance)
	}
	if store.ledgerCount() != 80 {
		t.Fatalf("expected 80 ledger entries, got %d", store.ledgerCount())
	}
}

func TestTransferService_Execute_OversubscribedWallet(t *testing.T) {
	store := newConcurrentStore()
	store.createWallet("wallet-a", 1000)
	for i := 0; i < 20; i++ {
		store.createWallet(fmt.Sprintf("wallet-b-%d", i), 0)
	}

	service := newConcurrentService(store)
	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	for i := 0; i < 20; i++ {
		req := TransferRequest{
			TransferID:     fmt.Sprintf("tx-%d", i),
			IdempotencyKey: fmt.Sprintf("idem-%d", i),
			FromWalletID:   "wallet-a",
			ToWalletID:     fmt.Sprintf("wallet-b-%d", i),
			Amount:         100,
			RequestHash:    fmt.Sprintf("hash-%d", i),
		}
		wg.Add(1)
		go func(req TransferRequest) {
			defer wg.Done()
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}(req)
	}
	wg.Wait()
	close(errCh)

	failures := 0
	for err := range errCh {
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientFunds) {
				failures++
			} else {
				t.Fatalf("expected only insufficient funds errors, got %v", err)
			}
		}
	}

	walletA, _ := store.GetWallet("wallet-a")
	if walletA.Balance < 0 {
		t.Fatalf("negative balance detected: %d", walletA.Balance)
	}
	if walletA.Balance != 0 {
		t.Fatalf("expected wallet-a balance 0, got %d", walletA.Balance)
	}
	if failures == 0 {
		t.Fatal("expected some insufficient funds failures")
	}
	if store.ledgerCount() != (20-failures)*2 {
		t.Fatalf("expected %d ledger entries, got %d", (20-failures)*2, store.ledgerCount())
	}
}

func TestTransferService_Execute_SimultaneousCreditsAndDebits(t *testing.T) {
	store := newConcurrentStore()
	store.createWallet("wallet-a", 500)
	store.createWallet("wallet-b", 500)
	store.createWallet("wallet-c", 500)

	reqs := []TransferRequest{}
	for i := 0; i < 4; i++ {
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("a2b-%d", i), IdempotencyKey: fmt.Sprintf("a2b-idem-%d", i), FromWalletID: "wallet-a", ToWalletID: "wallet-b", Amount: 50, RequestHash: fmt.Sprintf("hash-a2b-%d", i)})
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("b2c-%d", i), IdempotencyKey: fmt.Sprintf("b2c-idem-%d", i), FromWalletID: "wallet-b", ToWalletID: "wallet-c", Amount: 50, RequestHash: fmt.Sprintf("hash-b2c-%d", i)})
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("c2a-%d", i), IdempotencyKey: fmt.Sprintf("c2a-idem-%d", i), FromWalletID: "wallet-c", ToWalletID: "wallet-a", Amount: 50, RequestHash: fmt.Sprintf("hash-c2a-%d", i)})
	}
	for i := 0; i < 5; i++ {
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("a2c-%d", i), IdempotencyKey: fmt.Sprintf("a2c-idem-%d", i), FromWalletID: "wallet-a", ToWalletID: "wallet-c", Amount: 20, RequestHash: fmt.Sprintf("hash-a2c-%d", i)})
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("c2b-%d", i), IdempotencyKey: fmt.Sprintf("c2b-idem-%d", i), FromWalletID: "wallet-c", ToWalletID: "wallet-b", Amount: 20, RequestHash: fmt.Sprintf("hash-c2b-%d", i)})
		reqs = append(reqs, TransferRequest{TransferID: fmt.Sprintf("b2a-%d", i), IdempotencyKey: fmt.Sprintf("b2a-idem-%d", i), FromWalletID: "wallet-b", ToWalletID: "wallet-a", Amount: 20, RequestHash: fmt.Sprintf("hash-b2a-%d", i)})
	}

	service := newConcurrentService(store)
	var wg sync.WaitGroup
	errCh := make(chan error, len(reqs))

	for _, req := range reqs {
		wg.Add(1)
		go func(req TransferRequest) {
			defer wg.Done()
			_, err := service.Execute(context.Background(), req)
			errCh <- err
		}(req)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	}

	if store.totalBalance() != 1500 {
		t.Fatalf("expected total balance 1500, got %d", store.totalBalance())
	}
	for _, id := range []string{"wallet-a", "wallet-b", "wallet-c"} {
		wallet, _ := store.GetWallet(id)
		if wallet.Balance < 0 {
			t.Fatalf("expected non-negative balance for %s, got %d", id, wallet.Balance)
		}
	}
	if store.ledgerCount() != len(reqs)*2 {
		t.Fatalf("expected %d ledger entries, got %d", len(reqs)*2, store.ledgerCount())
	}
}

type walletRepoAdapter struct{ store *concurrentStore }

type transferRepoAdapter struct{ store *concurrentStore }

type ledgerRepoAdapter struct{ store *concurrentStore }

type idempotencyRepoAdapter struct{ store *concurrentStore }

func (r *walletRepoAdapter) Create(ctx context.Context, wallet *domain.Wallet) error {
	return r.store.walletRepoCreate(ctx, wallet)
}
func (r *walletRepoAdapter) GetByID(ctx context.Context, walletID string) (*domain.Wallet, error) {
	wallet, ok := r.store.GetWallet(walletID)
	if !ok {
		return nil, repository.ErrNotFound
	}
	return wallet, nil
}
func (r *walletRepoAdapter) GetByIDForUpdate(ctx context.Context, walletID string) (*domain.Wallet, error) {
	return r.store.walletRepoGetByIDForUpdate(ctx, walletID)
}
func (r *walletRepoAdapter) Update(ctx context.Context, wallet *domain.Wallet) error {
	return r.store.walletRepoUpdate(ctx, wallet)
}

func (r *transferRepoAdapter) Create(ctx context.Context, transfer *domain.Transfer) error {
	return r.store.transferRepoCreate(ctx, transfer)
}
func (r *transferRepoAdapter) GetByID(ctx context.Context, transferID string) (*domain.Transfer, error) {
	return r.store.transferRepoGetByID(ctx, transferID)
}
func (r *transferRepoAdapter) GetByIdempotencyKey(ctx context.Context, key string) (*domain.Transfer, error) {
	return r.store.transferRepoGetByIdempotencyKey(ctx, key)
}
func (r *transferRepoAdapter) UpdateStatus(ctx context.Context, transferID string, status domain.TransferStatus, failureReason string) error {
	return r.store.transferRepoUpdateStatus(ctx, transferID, status, failureReason)
}

func (r *ledgerRepoAdapter) CreateEntries(ctx context.Context, entries []domain.LedgerEntry) error {
	return r.store.ledgerRepoCreateEntries(ctx, entries)
}
func (r *ledgerRepoAdapter) GetByTransferID(ctx context.Context, transferID string) ([]domain.LedgerEntry, error) {
	return r.store.ledgerRepoGetByTransferID(ctx, transferID)
}
func (r *ledgerRepoAdapter) GetByWalletID(ctx context.Context, walletID string) ([]domain.LedgerEntry, error) {
	return r.store.ledgerRepoGetByWalletID(ctx, walletID)
}

func (r *idempotencyRepoAdapter) Create(ctx context.Context, record *repository.IdempotencyRecord) error {
	return r.store.idempotencyRepoCreate(ctx, record)
}
func (r *idempotencyRepoAdapter) GetByKey(ctx context.Context, key string) (*repository.IdempotencyRecord, error) {
	return r.store.idempotencyRepoGetByKey(ctx, key)
}
func (r *idempotencyRepoAdapter) UpdateResponse(ctx context.Context, key string, responseJSON []byte, statusCode int, status repository.IdempotencyStatus) error {
	return r.store.idempotencyRepoUpdateResponse(ctx, key, responseJSON, statusCode, status)
}
func (r *idempotencyRepoAdapter) UpdateStatus(ctx context.Context, key string, status repository.IdempotencyStatus) error {
	return r.store.idempotencyRepoUpdateStatus(ctx, key, status)
}
