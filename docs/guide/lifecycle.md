# The lifecycle

`payforge.Lifecycle` turns a verified event into what your code should do. For
subscriptions it computes the account's **entitlement**: which plan it is on
now.

```go
life := &payforge.Lifecycle{
    Spec:     spec,
    Store:    store,  // yours
    Sink:     sink,   // yours
    Provider: client, // the Stripe client; reads subscriptions
}
res, err := life.Handle(ctx, ev)
```

`Handle` returns an error **only when a retry could fix it**: the store, the
provider or the sink failed. Answer that with a 5xx so Stripe redelivers.
Anything else is a 2xx, and `res.Outcome` says what happened:

| Outcome | Meaning |
|---------|---------|
| `OutcomeApplied` | A sink was told |
| `OutcomeDuplicate` | Already processed; nothing changed |
| `OutcomeIgnored` | Not an event the lifecycle acts on, or no sink for it |
| `OutcomeSkipped` | Relevant but cannot be acted on, such as another app's event, a malformed event, or one from a connected account. `Reason` says why; log it |

An event for a customer **no account is known for** is skipped too, but is **not
recorded as seen**. Stripe often delivers the webhook before your checkout
return page has linked the customer to the account; when Stripe redelivers the
event after you have, it applies. An event with **no ID** is an error, because the
ID is what lets an event be processed once.

An event is recorded as seen **only after the sink applied it**. A failed attempt
is therefore retried, and a repeated one does nothing.

## Subscriptions are decided from Stripe, not from the event

After any subscription event the lifecycle lists the customer's subscriptions
from Stripe (that is `Provider`) and picks the highest plan that grants, the
later period end breaking a tie. The order events arrive in therefore does not
matter:

- A late "active" for a subscription that has since ended cannot grant the plan.
- A late "deleted" for an old subscription cannot drop an account that has a new
  one.

This costs one Stripe API call per subscription event. **Serialize per account:**
`Handle` does not lock, so two events for the same account handled at the same
time can apply in either order, and the later `Apply` may carry the older state.
Hold a lock per `Subject` (one handler at a time per account is enough), or give
your sink a version check. **Set `Provider` in production.** Without it the event alone decides, an ended subscription never
grants whatever status it carries, and an out-of-order event can leave a stale
plan.

## What grants a plan

- A subscription grants while it is `active`, `trialing` or `past_due`. Past due
  still grants because Stripe is retrying the payment and cutting access during
  the retries punishes a customer whose card is being fixed. Every other status,
  including one this library does not know, does not grant (`payforge.Grants`).
- **Only a price the spec has can grant.** The price's lookup key must name this
  app, a plan in the spec and an interval that plan prices. A price made by hand
  in the dashboard with the key `taskboard/enterprise/month` does not give anyone
  the arranged Enterprise plan. A subscription from before payforge, with no
  lookup key, is named by its metadata and counts only for a self-serve plan; a
  price that has some other lookup key names nothing, whatever the metadata says.
- With several lines or subscriptions, the highest tier wins; two plans in the
  same tier resolve by plan ID, so the order of the lines never matters.
- **Events from a seller's own Stripe account are never acted on** for
  subscriptions or refunds. A seller controls the objects in their own account,
  metadata and prices included, and their events arrive on the same handler as
  yours (that is why both secrets are configured). Only `account.updated` is
  taken from a connected account.
- With nothing that grants, the account is on the spec's default plan.

## The ports

You supply two, and optionally more for [marketplaces](marketplace.md).

```go
type Store interface {
    SubjectForCustomer(ctx context.Context, customerID string) (string, error) // ErrNotFound if none
    Seen(ctx context.Context, eventID string) (bool, error)
    MarkSeen(ctx context.Context, eventID string) error
}

type EntitlementSink interface {
    Apply(ctx context.Context, e payforge.Entitlement) error
}
```

`Store` remembers which events were processed, and maps a Stripe customer to your
account for events that do not name one. Usually the event names the account in
its metadata and the mapping is never used.

`Sink` updates your own record of the account's plan. Three rules:

1. **Set, do not change.** `Apply` receives the account's whole current
   entitlement, not a difference. Applying the same one twice must leave the same
   state.
2. **It will be called more than once** for the same event if an earlier attempt
   failed after it ran.
3. **Do not reject a drop.** An entitlement with `Granted: false` means the
   account is back on the default plan.

An `Entitlement` carries `Subject`, `Plan`, `Granted`, `SubscribedPlan`, `Status`,
`ValidUntil` (information; the status decides), `SubscriptionID`, `CustomerID`,
`Origin` and `EventID`. `Origin` lets a host that also sets plans by hand tell
where a change came from.

## Checking your own implementations

Write your store and sink against your database, then run the conformance suites
against them in your tests:

```go
func TestStoreConforms(t *testing.T) {
    payforgetest.Store(t, func(t *testing.T) (payforge.Store, func(customerID, subject string)) {
        s := newPostgresStore(t) // fresh and empty
        return s, s.LinkCustomer
    })
}

func TestSinkConforms(t *testing.T) {
    payforgetest.Sink(t, func(t *testing.T) (payforge.EntitlementSink, func(subject string) (payforge.Entitlement, bool)) {
        s := newPostgresSink(t)
        return s, s.Get
    })
}
```

They check replay safety, concurrent use, unknown customers, whole-state
replacement and independent accounts. `MemoryStore` and `MemorySink` pass them
and are fine for tests and for a host that does not need to survive a restart.

## Putting it together

[`examples/webhook`](https://github.com/plexusone/payforge/tree/main/examples/webhook) is the whole path: a handler that
verifies, calls `Handle`, and chooses the status code. Its test drives it with a
signed request and shows a redelivery applying once.
