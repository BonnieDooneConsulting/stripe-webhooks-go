package billing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"

	"github.com/BonnieDooneConsulting/stripe-webhooks-go/internal/webhook"
)

const secret = "whsec_test_secret"

type recorder struct {
	calls []string
}

func (r *recorder) Activate(_ context.Context, customerID, subscriptionID, reference string) error {
	r.calls = append(r.calls, "activate "+customerID+" "+subscriptionID+" "+reference)
	return nil
}

func (r *recorder) MarkPaymentFailed(_ context.Context, customerID, invoiceID string, _ int64) error {
	r.calls = append(r.calls, "failed "+customerID+" "+invoiceID)
	return nil
}

func (r *recorder) Deactivate(_ context.Context, customerID, subscriptionID string) error {
	r.calls = append(r.calls, "deactivate "+customerID+" "+subscriptionID)
	return nil
}

func deliver(t *testing.T, h http.Handler, eventType, object string) {
	t.Helper()
	payload := []byte(`{"id":"evt_` + eventType + `","object":"event","type":"` + eventType +
		`","api_version":"` + stripe.APIVersion + `","data":{"object":` + object + `}}`)
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{Payload: payload, Secret: secret})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200", eventType, rec.Code)
	}
}

func setup() (*webhook.Handler, *recorder) {
	h := webhook.NewHandler(secret, webhook.NewMemoryStore(time.Hour, time.Minute))
	r := &recorder{}
	Register(h, r)
	return h, r
}

func TestCheckoutCompletedActivatesAccount(t *testing.T) {
	h, r := setup()
	deliver(t, h, "checkout.session.completed",
		`{"id":"cs_1","object":"checkout.session","customer":"cus_1","subscription":"sub_1","client_reference_id":"user_42","payment_status":"paid"}`)

	want := "activate cus_1 sub_1 user_42"
	if len(r.calls) != 1 || r.calls[0] != want {
		t.Fatalf("calls = %v, want [%s]", r.calls, want)
	}
}

func TestUnpaidCheckoutWaits(t *testing.T) {
	h, r := setup()
	deliver(t, h, "checkout.session.completed",
		`{"id":"cs_2","object":"checkout.session","customer":"cus_1","payment_status":"unpaid"}`)

	if len(r.calls) != 0 {
		t.Fatalf("calls = %v, want none until the payment clears", r.calls)
	}
}

func TestInvoicePaymentFailedIsRecorded(t *testing.T) {
	h, r := setup()
	deliver(t, h, "invoice.payment_failed",
		`{"id":"in_1","object":"invoice","customer":"cus_1","attempt_count":2}`)

	if len(r.calls) != 1 || r.calls[0] != "failed cus_1 in_1" {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestSubscriptionDeletedDeactivates(t *testing.T) {
	h, r := setup()
	deliver(t, h, "customer.subscription.deleted",
		`{"id":"sub_1","object":"subscription","customer":"cus_1","status":"canceled"}`)

	if len(r.calls) != 1 || r.calls[0] != "deactivate cus_1 sub_1" {
		t.Fatalf("calls = %v", r.calls)
	}
}
