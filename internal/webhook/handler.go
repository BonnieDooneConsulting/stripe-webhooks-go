// Package webhook receives Stripe webhooks safely: it verifies signatures,
// skips events it has already processed, and tells Stripe to retry when your
// code fails.
package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

// maxBodyBytes caps the request body. Stripe events are well under this.
const maxBodyBytes = 64 << 10

// HandlerFunc processes one verified Stripe event. Return an error to have
// Stripe retry the delivery later.
type HandlerFunc func(ctx context.Context, event stripe.Event) error

// Handler is an http.Handler for a Stripe webhook endpoint.
type Handler struct {
	secret   string
	store    EventStore
	handlers map[stripe.EventType]HandlerFunc
	logger   *slog.Logger
	options  webhook.ConstructEventOptions
}

// Option configures a Handler.
type Option func(*Handler)

// WithLogger sets the logger. The default discards logs.
func WithLogger(l *slog.Logger) Option {
	return func(h *Handler) { h.logger = l }
}

// WithTolerance changes how old a signature timestamp may be. Stripe's
// default is five minutes, which is a good choice for most apps.
func WithTolerance(d time.Duration) Option {
	return func(h *Handler) { h.options.Tolerance = d }
}

// WithIgnoreAPIVersionMismatch accepts events whose API version differs from
// the one this stripe-go release was built for. Only turn this on if you've
// checked that the fields you read haven't changed between versions.
func WithIgnoreAPIVersionMismatch() Option {
	return func(h *Handler) { h.options.IgnoreAPIVersionMismatch = true }
}

// NewHandler returns a Handler that verifies events with secret (the
// endpoint's whsec_... signing secret) and records them in store.
func NewHandler(secret string, store EventStore, opts ...Option) *Handler {
	h := &Handler{
		secret:   secret,
		store:    store,
		handlers: make(map[stripe.EventType]HandlerFunc),
		logger:   slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// On registers fn for events of type t. Events with no registered handler
// are acknowledged and ignored, so Stripe stops sending them.
func (h *Handler) On(t stripe.EventType, fn HandlerFunc) {
	h.handlers[t] = fn
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}

	// Never trust the body until the signature checks out. Anyone who finds
	// this URL can POST a fake "payment succeeded" event to it.
	event, err := webhook.ConstructEventWithOptions(payload, r.Header.Get("Stripe-Signature"), h.secret, h.options)
	if err != nil {
		h.logger.Warn("rejected webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	log := h.logger.With("event_id", event.ID, "event_type", event.Type)

	fn, ok := h.handlers[event.Type]
	if !ok {
		log.Debug("ignoring unhandled event type")
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.Context()
	claimed, err := h.store.Claim(ctx, event.ID)
	if err != nil {
		log.Error("could not claim event", "error", err)
		http.Error(w, "temporary failure", http.StatusInternalServerError)
		return
	}
	if !claimed {
		// Already handled (or being handled). Saying 200 stops the retries.
		log.Info("skipping duplicate event")
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := fn(ctx, event); err != nil {
		log.Error("event handler failed, Stripe will retry", "error", err)
		if relErr := h.store.Release(ctx, event.ID); relErr != nil {
			log.Error("could not release event", "error", relErr)
		}
		http.Error(w, "handler failed", http.StatusInternalServerError)
		return
	}

	if err := h.store.Complete(ctx, event.ID); err != nil {
		// The work is done; failing here would only cause a duplicate run.
		log.Error("could not mark event complete", "error", err)
	}
	log.Info("processed event")
	w.WriteHeader(http.StatusOK)
}
