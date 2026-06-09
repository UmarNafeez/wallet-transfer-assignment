package application

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	repository "github.com/Robustrade/wallet-transfer-assignment/internal/respository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func connectTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	connString := os.Getenv("PGX_TEST_CONN_STRING")
	if connString == "" {
		connString = os.Getenv("DATABASE_URL")
	}
	if connString == "" {
		t.Skip("integration test requires PGX_TEST_CONN_STRING or DATABASE_URL")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("failed to connect to postgres: %v", err)
	}

	schemaName := fmt.Sprintf("test_app_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", pgx.Identifier{schemaName}.Sanitize())); err != nil {
		adminPool.Close()
		t.Fatalf("failed to create schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		adminPool.Close()
		t.Fatalf("failed to parse conn string: %v", err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("failed to connect with schema search path: %v", err)
	}

	if err := createSchemaObjects(ctx, pool); err != nil {
		pool.Close()
		adminPool.Close()
		t.Fatalf("failed to create schema objects: %v", err)
	}

	cleanup := func() {
		pool.Close()
		if _, err := adminPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE", pgx.Identifier{schemaName}.Sanitize())); err != nil {
			t.Logf("failed to drop schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	}

	return pool, cleanup
}

func createSchemaObjects(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, strings.TrimSpace(`
CREATE TYPE transfer_status AS ENUM ('PENDING', 'PROCESSED', 'FAILED');
CREATE TYPE ledger_entry_type AS ENUM ('DEBIT', 'CREDIT');

CREATE TABLE wallets (
    id UUID PRIMARY KEY,
    balance BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE transfers (
    id UUID PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    from_wallet_id UUID NOT NULL,
    to_wallet_id UUID NOT NULL,
    amount BIGINT NOT NULL,
    status transfer_status NOT NULL,
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY,
    transfer_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    entry_type ledger_entry_type NOT NULL,
    amount BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE idempotency_records (
    idempotency_key TEXT PRIMARY KEY,
    transfer_id UUID NOT NULL,
    request_hash TEXT NOT NULL,
    response_json JSONB NOT NULL,
    status_code INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`))
	return err
}

func TestTransferService_SelectForUpdateBlocksConcurrentTransfer(t *testing.T) {
	pool, cleanup := connectTestDB(t)
	defer cleanup()

	ctx := context.Background()
	walletRepo := repository.NewPostgresWalletRepository(pool)
	transferRepo := repository.NewPostgresTransferRepository(pool)
	ledgerRepo := repository.NewPostgresLedgerRepository(pool)
	idemRepo := repository.NewPostgresIdempotencyRepository(pool)
	manager := repository.NewPostgresTransactionManager(pool)
	service := NewTransferService(manager, walletRepo, transferRepo, ledgerRepo, idemRepo, nil)

	walletA := &domain.Wallet{ID: "11111111-1111-1111-1111-111111111111", Balance: 500}
	walletB := &domain.Wallet{ID: "22222222-2222-2222-2222-222222222222", Balance: 0}
	if err := walletRepo.Create(ctx, walletA); err != nil {
		t.Fatalf("failed to create wallet A: %v", err)
	}
	if err := walletRepo.Create(ctx, walletB); err != nil {
		t.Fatalf("failed to create wallet B: %v", err)
	}

	locked := make(chan struct{})
	releaseLock := make(chan struct{})
	txErrCh := make(chan error, 1)
	go func() {
		txErrCh <- manager.RunInTransaction(ctx, func(txCtx context.Context) error {
			lockedWallet, err := walletRepo.GetByIDForUpdate(txCtx, walletA.ID)
			if err != nil {
				return err
			}
			close(locked)
			<-releaseLock
			return walletRepo.Update(txCtx, lockedWallet)
		})
	}()

	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lock transaction to acquire row lock")
	}

	done := make(chan error, 1)
	go func() {
		_, err := service.Execute(ctx, TransferRequest{
			TransferID:     "33333333-3333-3333-3333-333333333333",
			IdempotencyKey: "idem-lock-1",
			FromWalletID:   walletA.ID,
			ToWalletID:     walletB.ID,
			Amount:         100,
			RequestHash:    "hash-lock-1",
		})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("transfer completed before lock released: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseLock)
	if err := <-txErrCh; err != nil {
		t.Fatalf("lock transaction failed: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("transfer failed after lock release: %v", err)
	}

	updatedA, err := walletRepo.GetByID(ctx, walletA.ID)
	if err != nil {
		t.Fatalf("failed to load wallet A: %v", err)
	}
	updatedB, err := walletRepo.GetByID(ctx, walletB.ID)
	if err != nil {
		t.Fatalf("failed to load wallet B: %v", err)
	}
	if updatedA.Balance != 400 {
		t.Fatalf("expected wallet A balance 400, got %d", updatedA.Balance)
	}
	if updatedB.Balance != 100 {
		t.Fatalf("expected wallet B balance 100, got %d", updatedB.Balance)
	}
}

func TestTransferService_IdempotencyConcurrentRetries(t *testing.T) {
	pool, cleanup := connectTestDB(t)
	defer cleanup()

	ctx := context.Background()
	walletRepo := repository.NewPostgresWalletRepository(pool)
	transferRepo := repository.NewPostgresTransferRepository(pool)
	ledgerRepo := repository.NewPostgresLedgerRepository(pool)
	idemRepo := repository.NewPostgresIdempotencyRepository(pool)
	manager := repository.NewPostgresTransactionManager(pool)
	service := NewTransferService(manager, walletRepo, transferRepo, ledgerRepo, idemRepo, nil)

	walletA := &domain.Wallet{ID: "44444444-4444-4444-4444-444444444444", Balance: 500}
	walletB := &domain.Wallet{ID: "55555555-5555-5555-5555-555555555555", Balance: 0}
	if err := walletRepo.Create(ctx, walletA); err != nil {
		t.Fatalf("failed to create wallet A: %v", err)
	}
	if err := walletRepo.Create(ctx, walletB); err != nil {
		t.Fatalf("failed to create wallet B: %v", err)
	}

	req := TransferRequest{
		TransferID:     "66666666-6666-6666-6666-666666666666",
		IdempotencyKey: "idem-concurrent-1",
		FromWalletID:   walletA.ID,
		ToWalletID:     walletB.ID,
		Amount:         100,
		RequestHash:    "hash-concurrent-1",
	}

	const concurrency = 5
	var wg sync.WaitGroup
	wg.Add(concurrency)
	results := make(chan *domain.Transfer, concurrency)
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			transfer, err := service.Execute(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			results <- transfer
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent transfer execution failed: %v", err)
	}

	transferIDs := map[string]int{}
	for transfer := range results {
		transferIDs[transfer.ID]++
	}
	if len(transferIDs) != 1 {
		t.Fatalf("expected exactly one unique transfer ID, got %d", len(transferIDs))
	}
	if count := transferIDs[req.TransferID]; count != concurrency {
		t.Fatalf("expected each goroutine to receive transfer ID %s, got %d instances", req.TransferID, count)
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM transfers WHERE idempotency_key = $1`, req.IdempotencyKey).Scan(&rowCount); err != nil {
		t.Fatalf("failed to count transfers: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 transfer row for idempotency key, got %d", rowCount)
	}

	updatedA, err := walletRepo.GetByID(ctx, walletA.ID)
	if err != nil {
		t.Fatalf("failed to load wallet A: %v", err)
	}
	updatedB, err := walletRepo.GetByID(ctx, walletB.ID)
	if err != nil {
		t.Fatalf("failed to load wallet B: %v", err)
	}
	if updatedA.Balance != 400 {
		t.Fatalf("expected wallet A balance 400 after one transfer, got %d", updatedA.Balance)
	}
	if updatedB.Balance != 100 {
		t.Fatalf("expected wallet B balance 100 after one transfer, got %d", updatedB.Balance)
	}
}
