package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

const testSecret = "whsec_test_secret"

func eventPayload(id string, t stripe.EventType) []byte {
	return fmt.Appendf(nil, `{"id":%q,"object":"event","type":%q,"api_version":%q,"data":{"object":{"id":"obj_123"}}}`,
		id, t, stripe.APIVersion)
}

func signedRequest(t *testing.T, payload []byte, secret string) *http.Request {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	return req
}

func serve(h http.Handler, req *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestValidEventIsDispatched(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	var got string
	h.On(stripe.EventTypeCheckoutSessionCompleted, func(_ context.Context, e stripe.Event) error {
		got = e.ID
		return nil
	})

	code := serve(h, signedRequest(t, eventPayload("evt_1", stripe.EventTypeCheckoutSessionCompleted), testSecret))

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got != "evt_1" {
		t.Fatalf("handler got event %q, want evt_1", got)
	}
}

func TestBadSignatureIsRejected(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	called := false
	h.On(stripe.EventTypeCheckoutSessionCompleted, func(context.Context, stripe.Event) error {
		called = true
		return nil
	})

	code := serve(h, signedRequest(t, eventPayload("evt_1", stripe.EventTypeCheckoutSessionCompleted), "whsec_wrong"))

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if called {
		t.Fatal("handler ran for an event with a bad signature")
	}
}

func TestMissingSignatureIsRejected(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe",
		bytes.NewReader(eventPayload("evt_1", stripe.EventTypeCheckoutSessionCompleted)))

	if code := serve(h, req); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestTamperedBodyIsRejected(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	req := signedRequest(t, eventPayload("evt_1", stripe.EventTypeCheckoutSessionCompleted), testSecret)
	// Same signature, different body.
	req.Body = httpBody(eventPayload("evt_2", stripe.EventTypeCheckoutSessionCompleted))

	if code := serve(h, req); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestDuplicateEventRunsOnce(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	calls := 0
	h.On(stripe.EventTypeInvoicePaymentFailed, func(context.Context, stripe.Event) error {
		calls++
		return nil
	})
	payload := eventPayload("evt_dup", stripe.EventTypeInvoicePaymentFailed)

	for i := range 3 {
		if code := serve(h, signedRequest(t, payload, testSecret)); code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i+1, code)
		}
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}
}

func TestFailedHandlerIsRetried(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	calls := 0
	h.On(stripe.EventTypeCustomerSubscriptionDeleted, func(context.Context, stripe.Event) error {
		calls++
		if calls == 1 {
			return errors.New("database unavailable")
		}
		return nil
	})
	payload := eventPayload("evt_retry", stripe.EventTypeCustomerSubscriptionDeleted)

	if code := serve(h, signedRequest(t, payload, testSecret)); code != http.StatusInternalServerError {
		t.Fatalf("first delivery: status = %d, want 500 so Stripe retries", code)
	}
	if code := serve(h, signedRequest(t, payload, testSecret)); code != http.StatusOK {
		t.Fatalf("retry: status = %d, want 200", code)
	}
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2", calls)
	}
}

func TestUnhandledEventTypeIsAcknowledged(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))

	code := serve(h, signedRequest(t, eventPayload("evt_other", "customer.created"), testSecret))

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	big := []byte(`{"id":"evt_big","pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`)

	if code := serve(h, signedRequest(t, big, testSecret)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", code)
	}
}

func TestOnlyPostIsAllowed(t *testing.T) {
	h := NewHandler(testSecret, NewMemoryStore(time.Hour, time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/webhooks/stripe", nil)

	if code := serve(h, req); code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", code)
	}
}

func TestStaleClaimCanBeTakenOver(t *testing.T) {
	s := NewMemoryStore(time.Hour, 30*time.Second)
	now := time.Now()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, _ := s.Claim(ctx, "evt_crash"); !ok {
		t.Fatal("first claim should succeed")
	}
	// The process "crashes" here: no Complete, no Release.
	if ok, _ := s.Claim(ctx, "evt_crash"); ok {
		t.Fatal("a fresh in-progress claim must block other deliveries")
	}

	now = now.Add(time.Minute)
	if ok, _ := s.Claim(ctx, "evt_crash"); !ok {
		t.Fatal("a stale claim should be taken over so the retry is processed")
	}
}

func TestCompletedEventIsNeverTakenOver(t *testing.T) {
	s := NewMemoryStore(time.Hour, 30*time.Second)
	now := time.Now()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	_, _ = s.Claim(ctx, "evt_done")
	_ = s.Complete(ctx, "evt_done")

	now = now.Add(time.Minute)
	if ok, _ := s.Claim(ctx, "evt_done"); ok {
		t.Fatal("a completed event must stay deduped after the claim timeout")
	}
}

func TestMemoryStoreForgetsAfterTTL(t *testing.T) {
	s := NewMemoryStore(time.Minute, 30*time.Second)
	now := time.Now()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, _ := s.Claim(ctx, "evt_1"); !ok {
		t.Fatal("first claim should succeed")
	}
	_ = s.Complete(ctx, "evt_1")
	if ok, _ := s.Claim(ctx, "evt_1"); ok {
		t.Fatal("claim within TTL should fail")
	}

	now = now.Add(2 * time.Minute)
	if ok, _ := s.Claim(ctx, "evt_1"); !ok {
		t.Fatal("claim after TTL should succeed")
	}
}
