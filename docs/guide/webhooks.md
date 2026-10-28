# Webhooks

Stripe tells you what happened by posting events to an endpoint you register in
the dashboard. payforge verifies each one and reduces it to a small `Event`.

```go
hook := stripe.Webhook{
    Mode:    client.Mode(), // or stripe.KeyMode(key); required
    Secrets: []string{platformSecret, connectSecret},
}
ev, err := hook.Read(r) // r is the *http.Request
if err != nil {
    // Answer 4xx or 5xx; never pass ev on: it is empty.
}
```

A complete handler is [`examples/webhook`](https://github.com/plexusone/payforge/tree/main/examples/webhook).

## What is checked

- **The signature comes first**, before any of the body is read. A payload is
  accepted if any one of the configured secrets verifies it, which is how the
  platform's endpoint and the connected-account endpoint share a handler.
- **A stale signature is refused** (older than five minutes; `Tolerance` changes
  it), as is a missing or malformed one.
- **The body is limited** to 1 MiB (`MaxBytes` changes it).
- A secret is never included in an error.
- The body is read before the signature is checked, up to the limit, so run the
  endpoint on a server with read timeouts (`http.Server.ReadTimeout`, or a
  `ReadHeaderTimeout` and a reverse proxy that limits slow bodies).
- **The mode must match.** `Mode` is the mode of your API key (`sk_test_` and
  `rk_test_` keys are test mode, any other is live). An event whose `livemode`
  differs comes back as `EventIgnored`, so a test-mode secret configured in
  production by mistake cannot change a live account; acknowledge it like any
  other ignored event. With `Mode` unset, `Read` returns an error for every
  event, so the mistake shows as 5xx responses instead of lost events.

## The two failures

Tell them apart, because they need different answers:

| Error | Meaning | Answer |
|-------|---------|--------|
| `payforge.ErrSignatureInvalid` | Not from Stripe, or stale | 400. A retry cannot fix it |
| `payforge.ErrPayloadTooLarge` | Body over the limit | 413 |
| Any other error | Stripe signed it but it cannot be read, or no secret or `Mode` is configured | 500: it is yours to fix, and Stripe will redeliver |

Treating an unreadable event as a bad signature would hide your own bug as an
attack.

## The event

| `Kind` | Stripe events | Carries |
|--------|---------------|---------|
| `EventSubscriptionCreated` / `Updated` / `Ended` | `customer.subscription.*` | `Subscription`: status, every line item's price and lookup key, period end, trial end |
| `EventCheckoutCompleted` | `checkout.session.completed`, `checkout.session.async_payment_succeeded` | `Checkout`: mode, whether `Paid`, payment intent, amount |
| `EventCheckoutFailed` | `checkout.session.async_payment_failed` | `Checkout`, with `Paid` false |
| `EventInvoicePaid` / `Failed` | `invoice.paid`, `invoice.payment_failed` | `Invoice` |
| `EventAccountUpdated` | `account.updated` | `AccountState` |
| `EventRefunded` | `charge.refunded` | `Refund` |
| `EventDisputeOpened` / `Closed` | `charge.dispute.created`, `charge.dispute.closed` | `Dispute`: charge, payment intent, amount, reason, status |
| `EventIgnored` | everything else, and any event of the other mode | only `ID` and `Type` |

Every event also has `ID`, `Type`, `Created`, `Livemode`, and the `App`, `Subject`,
`Plan` and `Product` read from the object's metadata. For a subscription with
no metadata, the app and plan come from its price's lookup key. `Metadata` is a
copy of the object's own metadata, so you can read keys of your own.

Only a few stable fields are read, into payforge's own structs, so an endpoint
pinned to a different Stripe API version than the library's still works. The
period end is read from the subscription or, on newer versions, from its items;
when the event gives none it is zero, never invented.

**Acknowledge an `EventIgnored`** with a 200 and move on. Stripe retries anything
else for days.

## Registering endpoints

- One endpoint for your own account's events: subscriptions, checkout
  (including `checkout.session.async_payment_failed`), invoices, refunds,
  disputes, and `account.updated` for your sellers' accounts.
- With a marketplace, a second endpoint for **events on connected accounts**,
  with its own secret. Both secrets go in `Secrets`, and
  [`CheckStartup`](index.md#free-mode) requires both.
- Use separate secrets for test and live mode, and never log them.

## Event order and replays

Stripe does not deliver events in order and delivers some more than once. Do
not make decisions from one event's contents alone; hand it to the
[lifecycle](lifecycle.md), which is built for exactly this.
