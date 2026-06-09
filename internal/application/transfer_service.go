package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/observability"
	repository "github.com/Robustrade/wallet-transfer-assignment/internal/respository"
)

/*
Sequence diagram:

Client -> TransferService.Execute
TransferService -> IdempotencyRepository.GetByKey
alt existing idempotency record

	TransferService -> TransferRepository.GetByID
	TransferService -> Client

else new transfer

	TransferService -> IdempotencyRepository.Create
	TransferService -> TransferRepository.Create
	TransferService -> WalletRepository.GetByIDForUpdate (first wallet)
	TransferService -> WalletRepository.GetByIDForUpdate (second wallet)
	TransferService -> WalletRepository.Update (source)
	TransferService -> WalletRepository.Update (destination)
	TransferService -> LedgerRepository.CreateEntries
	TransferService -> TransferRepository.UpdateStatus(PROCESSED)
	TransferService -> IdempotencyRepository.UpdateResponse
	TransferService -> Client

end
*/
var (
	ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")
)

type TransferRequest struct {
	TransferID     string
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
	RequestHash    string
}

type TransferService struct {
	transactionManager repository.TransactionManager
	walletRepo         repository.WalletRepository
	transferRepo       repository.TransferRepository
	ledgerRepo         repository.LedgerRepository
	idemRepo           repository.IdempotencyRepository
	metrics            *observability.Metrics
}

func NewTransferService(
	transactionManager repository.TransactionManager,
	walletRepo repository.WalletRepository,
	transferRepo repository.TransferRepository,
	ledgerRepo repository.LedgerRepository,
	idemRepo repository.IdempotencyRepository,
	metrics *observability.Metrics,
) *TransferService {
	return &TransferService{
		transactionManager: transactionManager,
		walletRepo:         walletRepo,
		transferRepo:       transferRepo,
		ledgerRepo:         ledgerRepo,
		idemRepo:           idemRepo,
		metrics:            metrics,
	}
}

func (s *TransferService) Execute(ctx context.Context, req TransferRequest) (*domain.Transfer, error) {
	start := time.Now()
	var result *domain.Transfer

	err := s.transactionManager.RunInTransaction(ctx, func(txCtx context.Context) error {
		transfer, err := s.executeInTransaction(txCtx, req)
		if err != nil {
			return err
		}
		result = transfer
		return nil
	})
	if s.metrics != nil {
		s.metrics.ObserveTransferDuration(time.Since(start).Seconds())
	}
	if err != nil {
		if s.metrics != nil {
			s.metrics.IncError("transfer_execute")
		}
		return nil, err
	}
	return result, nil
}

func (s *TransferService) TransferByID(ctx context.Context, transferID string) (*domain.Transfer, error) {
	if transferID == "" {
		return nil, domain.ErrEmptyTransferID
	}
	return s.transferRepo.GetByID(ctx, transferID)
}

func (s *TransferService) executeInTransaction(ctx context.Context, req TransferRequest) (*domain.Transfer, error) {
	if req.IdempotencyKey == "" {
		return nil, errors.New("idempotency key is required")
	}
	if req.TransferID == "" {
		return nil, errors.New("transfer ID is required")
	}

	existingRecord, err := s.idemRepo.GetByKey(ctx, req.IdempotencyKey)
	if err == nil {
		if s.metrics != nil {
			s.metrics.IncIdempotencyHit()
		}
		if existingRecord.RequestHash != req.RequestHash {
			return nil, ErrIdempotencyConflict
		}
		return s.transferRepo.GetByID(ctx, existingRecord.TransferID)
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	record := &repository.IdempotencyRecord{
		IdempotencyKey: req.IdempotencyKey,
		TransferID:     req.TransferID,
		RequestHash:    req.RequestHash,
		Status:         repository.IdempotencyStatusPending,
		ResponseJSON:   []byte("{}"),
		StatusCode:     0,
	}
	if err := s.idemRepo.Create(ctx, record); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			if s.metrics != nil {
				s.metrics.IncIdempotencyHit()
			}
			reloaded, loadErr := s.idemRepo.GetByKey(ctx, req.IdempotencyKey)
			if loadErr != nil {
				return nil, loadErr
			}
			if reloaded.RequestHash != req.RequestHash {
				return nil, ErrIdempotencyConflict
			}
			return s.transferRepo.GetByID(ctx, reloaded.TransferID)
		}
		return nil, err
	}

	transfer, err := domain.NewTransfer(req.TransferID, req.IdempotencyKey, req.FromWalletID, req.ToWalletID, req.Amount)
	if err != nil {
		return nil, err
	}
	if err := s.transferRepo.Create(ctx, transfer); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			existingTransfer, getErr := s.transferRepo.GetByIdempotencyKey(ctx, req.IdempotencyKey)
			if getErr != nil {
				return nil, getErr
			}
			return existingTransfer, nil
		}
		return nil, err
	}

	firstID, secondID := orderWalletIDs(req.FromWalletID, req.ToWalletID)
	firstWallet, err := s.walletRepo.GetByIDForUpdate(ctx, firstID)
	if err != nil {
		return nil, err
	}
	secondWallet, err := s.walletRepo.GetByIDForUpdate(ctx, secondID)
	if err != nil {
		return nil, err
	}

	fromWallet, toWallet := dereferenceWallets(req.FromWalletID, firstWallet, secondWallet)
	if fromWallet == nil || toWallet == nil {
		return nil, errors.New("wallet resolution failed")
	}

	if err := fromWallet.Debit(req.Amount); err != nil {
		_ = s.transferRepo.UpdateStatus(ctx, transfer.ID, domain.TransferStatusFailed, err.Error())
		_ = s.idemRepo.UpdateResponse(ctx, req.IdempotencyKey, []byte(fmt.Sprintf(`{"transferId":"%s","status":"FAILED"}`, transfer.ID)), 422, repository.IdempotencyStatusFailed)
		return transfer, err
	}
	if err := toWallet.Credit(req.Amount); err != nil {
		return nil, err
	}

	if err := s.walletRepo.Update(ctx, fromWallet); err != nil {
		return nil, err
	}
	if err := s.walletRepo.Update(ctx, toWallet); err != nil {
		return nil, err
	}

	entries := []domain.LedgerEntry{
		{ID: fmt.Sprintf("%s-debit", transfer.ID), TransferID: transfer.ID, WalletID: fromWallet.ID, EntryType: domain.LedgerEntryTypeDebit, Amount: req.Amount},
		{ID: fmt.Sprintf("%s-credit", transfer.ID), TransferID: transfer.ID, WalletID: toWallet.ID, EntryType: domain.LedgerEntryTypeCredit, Amount: req.Amount},
	}
	if err := s.ledgerRepo.CreateEntries(ctx, entries); err != nil {
		return nil, err
	}

	if err := s.transferRepo.UpdateStatus(ctx, transfer.ID, domain.TransferStatusProcessed, ""); err != nil {
		return nil, err
	}

	if err := s.idemRepo.UpdateResponse(ctx, req.IdempotencyKey, []byte(fmt.Sprintf(`{"transferId":"%s","status":"PROCESSED"}`, transfer.ID)), 200, repository.IdempotencyStatusCompleted); err != nil {
		return nil, err
	}

	transfer.Status = domain.TransferStatusProcessed
	transfer.FailureReason = ""
	return transfer, nil
}

func orderWalletIDs(a, b string) (first, second string) {
	if a < b {
		return a, b
	}
	return b, a
}

func dereferenceWallets(fromWalletID string, first, second *domain.Wallet) (fromWallet, toWallet *domain.Wallet) {
	if first.ID == fromWalletID {
		return first, second
	}
	return second, first
}
