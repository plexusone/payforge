# Checkout and the customer portal

All three operations return a URL on a page hosted by Stripe. Send the buyer
there; nothing about a card ever touches your server.

```go
client, err := stripe.New(spec, stripe.Config{SecretKey: key})
```

`stripe.Config` takes the secret key, an optional `HTTPClient` and
`MaxNetworkRetries` (default 2; retries reuse the idempotency key). The key is
never included in an error. A `*stripe.Client` implements `payforge.Billing`.

## A subject

Everything is for a **subject**: the account in your system that pays, usually an
organization.

```go
subject := payforge.Subject{ID: org.ID, Name: org.Name, Email: org.BillingEmail}
```

`ID` is required: 1 to 128 letters, digits and `. _ : -`, starting with a letter
or digit. Anything else is `payforge.ErrInvalidSubject`, because the ID is placed
in search queries and idempotency keys. The first call for a subject
creates a Stripe customer carrying the ID in metadata; later calls find it.
`client.EnsureCustomer(ctx, subject)` does only that. You may keep the customer
ID, but need not.

## Subscribing to a plan

```go
sess, err := client.CheckoutPlan(ctx, payforge.PlanCheckout{
    Subject:    subject,
    Plan:       "plus",
    Interval:   pricingspec.IntervalMonth,
    SuccessURL: "https://app.example.com/billing",
    CancelURL:  "https://app.example.com/pricing",
})
// redirect to sess.URL
```

- The success URL gets `session_id={CHECKOUT_SESSION_ID}` appended (Stripe fills
  it in), unless the URL already has the placeholder, so the page can say thanks.
- A plan with `trialDays` starts the trial **without asking for a card**, and the
  subscription is canceled at the end if the buyer added none.
- A subject that already holds a subscription that grants its plan is refused
  with `ErrAlreadySubscribed`, so two browser tabs cannot bill an organization
  twice. Send that subject to the portal instead.
- The same request returns the same session (the idempotency key is derived from
  the app, subject, plan, interval and URLs).

Paying does **not** change the account's plan by itself. The plan changes when
the subscription's webhook arrives; see [the lifecycle](lifecycle.md).

## Buying something once

```go
sess, err := client.CheckoutProduct(ctx, payforge.ProductCheckout{
    Subject:     subject,
    Product:     "sample_pack",
    Reference:   "order-8841",
    SuccessURL:  okURL,
    CancelURL:   cancelURL,
})
```

A fixed-price product is sold at its synced price, and giving an `AmountCents`
is `ErrAmountNotAllowed`. A seller-set product needs `AmountCents` at or above
the product's minimum (`ErrBelowMinimum`). Set `Reference` to your own order ID:
retrying with the same reference returns the same session, so a double click does
not make two.

## The customer portal

```go
url, err := client.Portal(ctx, customerID, "https://app.example.com/billing")
```

The portal is where a buyer updates a card, changes plan and cancels. Configure
which of those it allows in the Stripe dashboard. Changes made there come back as
subscription events.

## Mapping errors to responses

| Error | Meaning | Suggested response |
|-------|---------|--------------------|
| `ErrNotConfigured` | Free mode | 404 or hide the billing UI |
| `ErrInvalidSubject`, `ErrInvalidURL` | Bad input from your code | 400 or 500 |
| `ErrUnknownPlan`, `ErrUnknownProduct` | Not in the spec | 404 |
| `ErrNotSelfServe`, `ErrNoPrice` | Cannot be bought here | 400 |
| `ErrBelowMinimum`, `ErrAmountNotAllowed` | Bad amount | 400 |
| `ErrAlreadySubscribed` | Already subscribed | 409, send to the portal |
| `ErrNotSynced` | The catalog was not synced to this account | 500, and run [sync](catalog.md#syncing) |

Test with `errors.Is`. Any other error is Stripe's, wrapped with what was
attempted.
