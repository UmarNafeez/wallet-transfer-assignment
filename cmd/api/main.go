package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Robustrade/wallet-transfer-assignment/internal/application"
	"github.com/Robustrade/wallet-transfer-assignment/internal/observability"
	repository "github.com/Robustrade/wallet-transfer-assignment/internal/respository"
	transporthttp "github.com/Robustrade/wallet-transfer-assignment/internal/transport/http"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("unable to connect to database: %v", err)
	}
	defer pool.Close()

	walletRepo := repository.NewPostgresWalletRepository(pool)
	transferRepo := repository.NewPostgresTransferRepository(pool)
	ledgerRepo := repository.NewPostgresLedgerRepository(pool)
	idemRepo := repository.NewPostgresIdempotencyRepository(pool)
	txManager := repository.NewPostgresTransactionManager(pool)

	logger := observability.NewLogger(os.Stdout)
	metrics := observability.NewMetrics(prometheus.NewRegistry())

	walletService := application.NewWalletService(walletRepo, ledgerRepo)
	transferService := application.NewTransferService(txManager, walletRepo, transferRepo, ledgerRepo, idemRepo, metrics)
	healthService := application.NewHealthServiceFromPgxPool(pool)
	server := transporthttp.NewServer(walletService, transferService, healthService)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}))
	handler := observability.HTTPMiddleware(logger, metrics)(transporthttp.WithCorrelationID(mux))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	log.Printf("starting wallet transfer API on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
