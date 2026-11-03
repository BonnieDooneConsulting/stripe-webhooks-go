# stripe-webhooks-go

A small, readable Go service that handles Stripe webhooks the way I'd want them handled in production: signatures verified, duplicate events skipped, and failures handed back to Stripe so it retries.

I wrote this because the same few webhook bugs show up in almost every Stripe codebase I look at. If you've read [5 Stripe Integration Mistakes That Cost Startups Money](https://bonniedoone.ai/blog/stripe-integration-mistakes), this is that post as working code.

## What's in here

- **Signature verification.** Every request is checked against your endpoint's signing secret before anything else happens. Without this, anyone who finds your URL can send a fake "payment succeeded" event.
- **Idempotent processing.** Stripe delivers events at least once, so you'll see repeats. Each event ID is claimed before it's processed, and a repeat gets a polite `200` and nothing else.
- **Retries that actually retry.** If your handler fails, the claim is released and Stripe gets a `500`, so it tries again later. If it succeeds, Stripe gets a `200` and stops.
- **Example billing handlers** for `checkout.session.completed`, `invoice.payment_failed`, and `customer.subscription.deleted`, written against a small `Accounts` interface you can point at your own database.
- **An idempotency key example** for creating PaymentIntents, so a retried request can't charge someone twice.
- Tests for all of the above, a Dockerfile, and CI.

## Quick start

You'll need Go 1.24+ and the [Stripe CLI](https://docs.stripe.com/stripe-cli).

```sh
# 1. Forward test-mode events to your machine. This prints a whsec_... secret.
#    --latest sends events in the newest API version, which is what stripe-go expects.
stripe listen --latest --forward-to localhost:8080/webhooks/stripe

# 2. In another terminal, start the server with that secret.
STRIPE_WEBHOOK_SECRET=whsec_... make run

# 3. Send a test event.
stripe trigger checkout.session.completed
```

You should see the event verified and processed in the server logs. If you get a `400` that mentions the API version, you left off `--latest`: stripe-go only accepts events in the API version it was built for (see "Taking it to production" below). Trigger the same event again from the Stripe dashboard ("Resend") and you'll see it skipped as a duplicate.

## How it fits together

```
cmd/server            wires everything up and runs the HTTP server
internal/webhook      verification, dedupe, retry behavior (the reusable part)
internal/billing      example handlers that turn events into account changes
internal/payments     creating a PaymentIntent with an idempotency key
```

Registering a handler looks like this:

```go
hook := webhook.NewHandler(secret, store, webhook.WithLogger(logger))

hook.On(stripe.EventTypeInvoicePaymentFailed, func(ctx context.Context, e stripe.Event) error {
	var invoice stripe.Invoice
	if err := json.Unmarshal(e.Data.Raw, &invoice); err != nil {
		return err
	}
	return notifyCustomer(ctx, invoice.Customer.ID)
})
```

Return an error and Stripe will retry. Return `nil` and you're done.

## Taking it to production

The in-memory event store is there so the example runs with no setup. It forgets everything on restart and isn't shared between instances, so swap it for a database before you deploy. A table with a unique key is all you need:

```sql
CREATE TABLE stripe_events (
  event_id     text PRIMARY KEY,
  status       text NOT NULL,          -- 'in_progress' or 'done'
  updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Claim: succeeds for exactly one delivery of an event. An in-progress
-- claim older than a minute is taken over, so a crash mid-event doesn't
-- block Stripe's retries forever.
INSERT INTO stripe_events (event_id, status) VALUES ($1, 'in_progress')
ON CONFLICT (event_id) DO UPDATE
  SET status = 'in_progress', updated_at = now()
  WHERE stripe_events.status = 'in_progress'
    AND stripe_events.updated_at < now() - interval '1 minute';
-- One row affected means you own the event. Zero means skip it.
```

A few other things I'd do before going live:

- Keep processed event IDs for longer than Stripe's retry window (up to three days).
- Keep handlers fast. Stripe expects a reply within seconds, so hand slow work to a queue. If you want that built in, see the EventBridge and SQS version: [stripe-eventbridge-sqs-go](https://github.com/BonnieDooneConsulting/stripe-eventbridge-sqs-go).
- Create your webhook endpoint in the Stripe dashboard with the same API version stripe-go expects (it's `stripe.APIVersion` in the library), and bump both together when you upgrade. Leave the version check on unless you've confirmed the fields you read haven't changed. `WithIgnoreAPIVersionMismatch` is there for when you need it.

## Running the tests

```sh
make test   # go test -race ./...
make lint   # golangci-lint v2
```

## License

MIT. Use it, change it, ship it.

---

Built by [Bonnie Doone Consulting](https://bonniedoone.ai). Have a Stripe integration that keeps breaking, or one you're about to build? [Let's talk](https://bonniedoone.ai/services/api-stripe-integrations).
