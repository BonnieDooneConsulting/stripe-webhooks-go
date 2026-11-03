// Package payments shows how to create charges that can't double-bill a
// customer, even when a request is retried.
package payments

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v87"
)

// IdempotencyKey returns the key used when charging for an order. It has to
// be the same every time you retry the same charge, so derive it from your
// own order ID rather than generating a random one per request.
func IdempotencyKey(orderID string) string {
	return "order-" + orderID + "-payment-intent"
}

// CreatePaymentIntent charges for an order. If the network drops and you call
// it again for the same order, Stripe returns the original PaymentIntent
// instead of creating a second charge.
func CreatePaymentIntent(ctx context.Context, sc *stripe.Client, orderID string, amount int64, currency string) (*stripe.PaymentIntent, error) {
	if orderID == "" {
		return nil, fmt.Errorf("order ID is required")
	}
	params := &stripe.PaymentIntentCreateParams{
		Amount:   stripe.Int64(amount),
		Currency: stripe.String(currency),
		AutomaticPaymentMethods: &stripe.PaymentIntentCreateAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	}
	params.AddMetadata("order_id", orderID)
	params.SetIdempotencyKey(IdempotencyKey(orderID))

	pi, err := sc.V1PaymentIntents.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create payment intent for order %s: %w", orderID, err)
	}
	return pi, nil
}
