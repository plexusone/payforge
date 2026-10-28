# Checkout and the customer portal

All three operations return a URL on a page hosted by Stripe. Send the buyer
there; nothing about a card ever touches your server.

```go
client, err := stripe.New(spec, stripe.Config{
    SecretKey:       key,
    AllowedURLHosts: []string{"app.example.com"},
})
```

`stripe.Config` takes the secret key, `AllowedURLHosts`, an optional `HTTPClient`,
an optional `Locker` and `MaxNetworkRetries` (default 2, and zero means the default, so retries cannot
be turned off; they reuse the idempotency key). The key is never included in an
error. A `*stripe.Client` implements `payforge.Billing`.

**Give every call a context with a deadline.** Without one a call can wait for
the library's own timeout and its retries, which is minutes, and `Sync` makes many
calls in a row.

## URLs

Success, cancel, return and refresh URLs must be `https`, and may not carry a
user name or password. **Set `AllowedURLHosts` to your own hostnames**: a URL
that names any other host is refused with `ErrInvalidURL`. With a test-mode key,
`localhost`, `127.0.0.1` and `::1` are also accepted, over `http` too, even when
the list does not name them, for local development. With a live key they are
never accepted: the client refuses a list that names one when it is made, and a
URL on one is refused, because a live checkout that returns the buyer to their
own machine is always a misconfiguration. Otherwise a URL built from anything a user sent could
send a buyer through a genuine Stripe checkout to a page of an attacker's
choosing, holding the session ID. Entries are bare hostnames, compared without
case; a scheme, port or wildcard is refused when the client is made. A client
that only [syncs the catalog](catalog.md#syncing) may leave the list empty.

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
  with `ErrAlreadySubscribed`. Send that subject to the portal instead.
- **Only the newest plan checkout can be paid.** Before creating a session,
  payforge expires the subject's other open plan checkouts for the app, so two
  tabs cannot both subscribe. If one cannot be expired, because the buyer is
  completing it right now, the call fails and nothing is created; a retry then
  sees the subscription. Two calls at the very same moment can still miss each
  other unless `stripe.Config.Locker` is set, which runs them in turn per
  subject (see [the lifecycle](lifecycle.md#subscriptions-are-decided-from-stripe-not-from-the-event)).
- A network retry returns the same session (the idempotency key is derived from
  the app, subject, plan, interval, price, URLs and any session just expired).
  Stripe replays a key for a day, so a session that was completed or has expired
  comes back without a URL; payforge then creates a new one, and a buyer who
  subscribes, cancels and returns the same day gets a working page.

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
