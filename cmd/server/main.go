// Command server runs a Stripe webhook endpoint at /webhooks/stripe.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BonnieDooneConsulting/stripe-webhooks-go/internal/billing"
	"github.com/BonnieDooneConsulting/stripe-webhooks-go/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	secret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if secret == "" {
		logger.Error("STRIPE_WEBHOOK_SECRET is required (it starts with whsec_)")
		os.Exit(1)
	}
	addr := ":" + envOr("PORT", "8080")

	// Remember processed events for longer than Stripe's three-day retry
	// window, and let a claim go stale if a crash leaves it in progress.
	store := webhook.NewMemoryStore(4*24*time.Hour, time.Minute)
	hook := webhook.NewHandler(secret, store, webhook.WithLogger(logger))
	billing.Register(hook, billing.LogAccounts{Logger: logger})

	mux := http.NewServeMux()
	mux.Handle("/webhooks/stripe", hook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
