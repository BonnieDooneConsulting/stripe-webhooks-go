// Package billing turns Stripe events into changes in your own app: turning
// access on after checkout, flagging failed payments, and turning access off
// when a subscription ends.
package billing

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stripe/stripe-go/v87"

	"github.com/BonnieDooneConsulting/stripe-webhooks-go/internal/webhook"
)

// Accounts is the part of your app that billing events change. Swap in your
// real database or service here.
type Accounts interface {
	Activate(ctx context.Context, customerID, subscriptionID, reference string) error
	MarkPaymentFailed(ctx context.Context, customerID, invoiceID string, attempt int64) error
	Deactivate(ctx context.Context, customerID, subscriptionID string) error
}

// Register wires the billing handlers onto a webhook handler.
func Register(h *webhook.Handler, accounts Accounts) {
	h.On(stripe.EventTypeCheckoutSessionCompleted, checkoutCompleted(accounts))
	h.On(stripe.EventTypeInvoicePaymentFailed, invoicePaymentFailed(accounts))
	h.On(stripe.EventTypeCustomerSubscriptionDeleted, subscriptionDeleted(accounts))
}

func checkoutCompleted(accounts Accounts) webhook.HandlerFunc {
	return func(ctx context.Context, event stripe.Event) error {
		var session stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
			return fmt.Errorf("decode checkout session: %w", err)
		}
		// Async payment methods (like bank debits) can complete checkout
		// before the money arrives. Wait for checkout.session.async_payment_succeeded
		// in that case instead of granting access now.
		if session.PaymentStatus == stripe.CheckoutSessionPaymentStatusUnpaid {
			return nil
		}
		return accounts.Activate(ctx, idOf(session.Customer), subscriptionID(session.Subscription), session.ClientReferenceID)
	}
}

func invoicePaymentFailed(accounts Accounts) webhook.HandlerFunc {
	return func(ctx context.Context, event stripe.Event) error {
		var invoice stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
			return fmt.Errorf("decode invoice: %w", err)
		}
		return accounts.MarkPaymentFailed(ctx, idOf(invoice.Customer), invoice.ID, invoice.AttemptCount)
	}
}

func subscriptionDeleted(accounts Accounts) webhook.HandlerFunc {
	return func(ctx context.Context, event stripe.Event) error {
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			return fmt.Errorf("decode subscription: %w", err)
		}
		return accounts.Deactivate(ctx, idOf(sub.Customer), sub.ID)
	}
}

func idOf(c *stripe.Customer) string {
	if c == nil {
		return ""
	}
	return c.ID
}

func subscriptionID(s *stripe.Subscription) string {
	if s == nil {
		return ""
	}
	return s.ID
}
