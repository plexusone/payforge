# payforge: Technical Requirements

**Initiative:** `INIT-PAYFORGE-001`
**Repository:** `github.com/plexusone/payforge`

Status: **draft**. The model below is proposed from two existing Stripe
integrations that this library replaces; it is frozen only after the review
and design items in Phase 1 of the roadmap.

## Scope

A Go library with three layers, each usable without the next:

```text
host (a product, or a platform that hosts products)
      │
      ▼
payforge            lifecycle · ports · provider plan (reads the pricing spec)
      │      (provider-neutral core: no payment SDK)
      ▼
payforge/stripe     checkout · portal · connect · webhooks · sync
      │
      ▼
Stripe API
```

The core never imports a payment SDK. The Stripe adapter is the only package
that does, so a host that only reads the pricing spec and checks limits does not build
the Stripe client.

## The pricing spec

payforge does not define the description of a product's pricing and packaging.
It reads one, from
[`systemspec-pricingpackaging`](https://github.com/plexusone/systemspec-pricingpackaging)
(`INIT-SYSTEMSPECPRICINGPACKAGING-001`): tiers, plans and prices, features with
levels, limits, one-time products, a marketplace fee and the entitlements each
tier grants. That repository owns the types, the schema, validation, the plan,
feature and limit helpers, the pricing view and the diff. This one owns what
happens at the payment provider and in the subscription lifecycle.

What payforge needs from the spec:

- The plans with their prices by interval, to create provider prices.
- Each plan's tier, to tell the host what a paid subscription means.
- The default plan, for free mode and for anything unrecognised.
- The marketplace fee and the one-time products, for Connect sales.
- The product identifier, to tag everything written to the provider.

```go
spec, err := pricingspec.Load(r)     // validated by the spec package
plan := spec.PlanOrDefault("pro")
```

A host that cannot be given what it needs (billing is configured but a
self-serve plan has no price) fails at startup.

## Provider sync

`stripe.Sync(ctx, client, spec)` makes a Stripe account match the spec:

- A product per plan and per one-time product, with metadata
  `payforge_app=<app>` and `payforge_plan=<id>`.
- A price per interval, with the **lookup key**
  `<app>/<plan>/<interval>` (and `<app>/<product>` for one-time products).
- A price is immutable in Stripe, so a changed amount creates a new price,
  moves the lookup key to it and leaves the old one in place for existing
  subscribers. Sync never deletes and never edits an amount.
- Sync is idempotent and has a dry-run mode that reports what it would do.
- A product cannot be found by key at Stripe, so its ID is derived from the key
  (`payforge_<app>_<plan>`), which Stripe lets the creator choose. A price is
  found by lookup key.
- A price counts as unchanged only when its amount, currency, interval and
  product all match. Any difference creates a new price with
  `transfer_lookup_key`, which moves the key to it.
- Sync changes a product's name and payforge metadata when they differ, and
  nothing else about it. It adopts an object that carries no app metadata, and
  stops with an error on one that names another app, or on an archived product:
  it never brings an archived object back.
- If a step fails, the report holds the steps already taken along with the
  error, so a retry knows where it stands. Plans removed from the spec leave
  their products and prices in the account.

Checkout then names a plan and interval and looks the price up by key, so no
price ID appears in configuration.

## Checkout, portal and customers

```go
type Billing interface {
    CheckoutPlan(ctx, PlanCheckout) (Session, error)       // subscription
    CheckoutProduct(ctx, ProductCheckout) (Session, error) // one-time
    Portal(ctx, customerID, returnURL string) (string, error)
    EnsureCustomer(ctx, Subject) (customerID string, err error)
}
```

- `Subject` is the host's organization or account: an ID, a name and a billing
  email. The customer carries the host's ID in metadata, and creation is
  idempotent on it.
- Checkout carries the subject and plan in metadata on the session and on the
  subscription, so each event can be matched without the session.
- Return URLs are supplied by the host and carry the session ID. They must be
  absolute `http` or `https` addresses; anything else is refused before a
  request is made.
- `EnsureCustomer` searches the provider by the app and subject metadata, then
  creates with an idempotency key. The provider's search can lag a creation by
  up to a minute; the key covers that gap.
- A plan checkout is refused with `ErrAlreadySubscribed` when the customer
  already holds a subscription that grants (`payforge.Grants`), so two browser
  tabs cannot bill an account twice. Its idempotency key is derived from the
  app, subject, plan, interval and return URLs, so the same request returns
  the same session and a different one is its own.
- A plan with `trialDays` starts the trial without asking for a card, and the
  subscription is canceled at the end of the trial if the buyer added none.
- A one-time purchase of a seller-set product takes the amount from the
  caller and refuses one under the product's minimum; a fixed product takes
  its synced price and refuses a stray amount. A caller-supplied `Reference`
  makes a retry return the same session.

## The Stripe library

The adapter uses the official `stripe-go` library rather than a client of our
own. This is the code that gets the product paid, so the request shapes,
retries with idempotency keys, pagination and error types are better taken from
the maintainer than rewritten. One major version is pinned across payforge, and
the adapter reads only a few stable fields of what comes back, so a newer API
version on an account does not break it. Tests stand in for the API with an HTTP
server, by pointing the library's backend at it.

## Connect (marketplace)

```go
type Connect interface {
    CreateAccount(ctx, seller Subject, country string) (accountID string, err error)
    OnboardingLink(ctx, accountID, refreshURL, returnURL string) (string, error)
    Account(ctx, accountID string) (AccountState, error)  // charges, payouts, details
    CheckoutSale(ctx, Sale) (Session, error)              // destination charge
}
```

- **Accounts** are Express, because the provider hosts onboarding. Creation uses
  an idempotency key from the app and the seller, so a repeat returns the same
  account. Stripe cannot search accounts, so a host stores the ID. The country
  is the seller's two-letter code and cannot be changed afterwards.
- **A sale** is a destination charge made on the seller's behalf: the seller is
  the merchant on the buyer's statement, the proceeds are transferred to the
  seller's account, and the platform's fee is the application fee. The sale
  carries the buyer, the seller's account and plan, a product from the spec and a
  reference that makes a retry return the same session.
- **The fee** is the seller plan's `platformFeeBps` when it sets one, else the
  marketplace's, applied to what the buyer pays and **rounded down** to a cent.
  The seller never receives less than the exact share and the platform never
  takes more. A fee of zero sends no application fee. A spec with no
  marketplace cannot create accounts or sell.
- **A seller who cannot take charges** is refused with `ErrSellerNotReady`
  before any checkout is made; the account is read from the provider for that,
  so it costs one API call per sale.
- A sale uses the same product lines as a purchase: a fixed price at its synced
  price, a seller-set product at the amount given, which must reach the minimum.
  Its objects carry the seller's account under `payforge_seller`.
- Account IDs and URLs are checked before a request is made. URLs must be https
  (http for localhost) without credentials, on a host in `AllowedURLHosts` (only localhost when it is empty).
- A sale pays only an account carrying this app's `payforge_app` metadata
  (`ErrForeignAccount`), and the seller plan that sets the fee comes from the host.

## Webhooks and events

```go
ev, err := stripe.ParseEvent(payload, signatureHeader, secret)
```

- The signature is verified and the payload reduced to a small `Event`: a
  checkout completion, a subscription created, updated or ended, an invoice
  paid or failed, a Connect account update, or a refund. Other event types
  return an `Event` with no body so the host can acknowledge and ignore them.
- Only a few stable fields are read, so an endpoint pinned to another API
  version still works.
- `stripe.Webhook{Secrets, LegacyPrefixes}` verifies against any one of several
  secrets (the platform's endpoint and the connected-account endpoint), checks the
  signature before reading the payload, and refuses a stale signature. A
  signature failure is `ErrSignatureInvalid`; a payload that verifies but cannot
  be read is a different error, so a host can answer 400 to one and 500 to the
  other. `Read` limits the body to 1 MiB.
- The event keeps every line item of a subscription, not only the first, and
  never invents a period end: when the event does not give one it is zero. The
  period and the invoice's subscription are read from both places the API has put
  them, so an account on a newer API version works.
- App, subject and plan come from payforge's metadata, then from configured
  legacy prefixes (`<prefix>_organization_id`, `<prefix>_plan`), then for the app
  and plan from a price's lookup key. The object's metadata is kept on the event
  for a host's own keys. An event whose app is not the spec's belongs to another
  product sharing the account.
- A subscription **grants** its plan while `active`, `trialing` or
  `past_due` (the provider is retrying a payment), and stops granting when it
  is ended or `unpaid` or `canceled`.

## Lifecycle and ports

```go
type EntitlementSink interface {
    Apply(ctx context.Context, e Entitlement) error
}

type Store interface {                  // the host's persistence
    SubjectForCustomer(ctx, customerID string) (string, error) // ErrNotFound if none
    Seen(ctx, eventID string) (bool, error)
    MarkSeen(ctx, eventID string) error
}

type SubscriptionReader interface {     // the Stripe client implements it
    Subscriptions(ctx, customerID string) ([]SubscriptionState, error)
}

type Lifecycle struct {
    Spec *pricingspec.Spec; Store Store; Sink EntitlementSink
    Provider SubscriptionReader; LegacyPrices map[string]string
}

func (l *Lifecycle) Handle(ctx context.Context, ev Event) (Result, error)
```

- `Handle` returns an error only for something a retry could fix (the store, the
  provider or the sink), which the host answers with a 5xx so the provider
  redelivers. Everything else is a `Result` the host acknowledges: `applied`,
  `duplicate`, `ignored` (not a subscription event) or `skipped` (relevant but
  not actionable, such as another product's event or an unknown account), with a
  reason.
- It resolves the account from the event's metadata, then from the host's
  `Store.SubjectForCustomer`.
- **The provider decides, not the event.** After any subscription event, with a
  `Provider` set, the account's entitlement is computed from all of the
  customer's subscriptions as the provider holds them now: the highest plan among
  those that grant, the later period end breaking a tie, the default plan when
  none grants. A late `updated` that says `active` for a subscription that has
  since ended, a late `deleted` for an old subscription while a new one is live,
  and a replay all give the same answer, whatever order events arrived in.
  Without a `Provider` the event alone decides, and an ended subscription is
  never granted whatever status the event carries.
- **Only a price the spec has can grant.** A line grants through its lookup key
  only if the key names this app, a plan of the spec and an interval that plan
  has a price for. A price made by hand, another app's price, an arranged plan or
  a free plan resolves to nothing. A price from before payforge with no key
  grants only when the host lists it in `LegacyPrices`, for the plan listed
  there; metadata never names the plan. With several lines the highest plan wins.
- `Entitlement` carries the plan the account should be on (the default plan when
  nothing grants), whether it is granted, the subscribed plan and its status, the
  period end as information, the subscription and customer, an `Origin` and the
  event ID, so a host that sets a plan by hand can see where a change came from
  and decide whether to let it through.
- An event is recorded as seen only after the sink applied it, so a failed
  attempt is retried; `Apply` receives the account's whole current entitlement,
  not a change, so applying it twice leaves the same state.
- Checkout and invoice events are ignored by the lifecycle: a subscription's
  checkout and renewals are applied from its own subscription events, and a
  one-time sale is the host's own to record when its checkout completes.
- **Connected accounts are not trusted for subscriptions or refunds.** A seller
  controls the objects in their own account, metadata and prices included, and
  their events arrive on the shared handler. An event carrying a connected
  account is skipped for subscriptions and refunds; only `account.updated` is
  taken from one, and it is applied from the provider's current state.
- **Skips.** An event for a customer no account is known for is skipped without
  being recorded, so its redelivery applies once the host links the customer; any
  other skip, including a malformed event, is recorded. An event with no ID is an
  error. The lifecycle does not serialize: the host holds a lock per account.
- **Refunds** go to an optional `SaleSink`. Only a charge that carries a payforge
  product is a sale; a refund of a subscription's invoice, or of another app's
  charge, is skipped. The sink receives the buyer, the seller, the product, the
  charge and payment intent to match the host's own record, and the refunded
  total. A full refund ends the buyer's access to what was bought and the host
  keeps the data. The total only grows, and a sink keeps the highest it has seen
  and treats a full refund as permanent, so a late event for an earlier partial
  refund cannot reopen a sale.
- **Connected account changes** go to an optional `SellerSink`, which receives
  the whole current state: whether the account can take charges and be paid
  out, and whether the details are submitted. With an `AccountReader` (the
  Stripe client is one) the provider's state now is applied, not the event's, so
  a late event cannot turn charges back on or off. An account without payforge's
  metadata, or another app's, is skipped.
- Without a sink, its events are ignored and not recorded, so a host can add one
  later. As for entitlements, an event is recorded as seen only after the sink
  applied it.

Persistence, authorization and audit are interfaces the host supplies. The
library includes in-memory implementations (`MemoryStore`, `MemorySink`) and the
`payforgetest` package, a conformance suite a host runs against its own store
and sink.

## Billing not configured

A host that has no provider configured (no secret key, as in development or a
deployment that does not charge) runs in **free mode**: every account is on the
spec's default plan. This is explicit in the API, not a silent
elevation: `payforge.Disabled(spec)` returns a billing implementation whose
plan lookups return the default plan, whose checkout calls return
`ErrNotConfigured`, and which a host can detect to hide upgrade prompts. A
host that wants everything unlocked for development makes that its own
decision, for example by naming a different default.

Turning billing on later does not change existing accounts' plans: it is the
default plan until a subscription says otherwise. A host that has granted
accounts more than the default while billing was off decides how to treat
them (a grace period or a migration), since the library cannot know.

## Metadata and revenue reporting

Everything written to the provider carries `payforge_app`, and subscriptions
and sales also carry the subject and plan. Several products can therefore
share one account, or each have their own; a report that reads revenue out of
the account can group by `payforge_app` and `payforge_plan`. payforge does not
read revenue.

## Errors and secrets

- Sentinel errors for the cases a host handles: not configured, invalid subject
  or return URL, unknown plan or product, not self-serve, no price for the
  interval, amount below the minimum or given for a fixed price, catalog not
  synced, already subscribed, signature invalid, event already processed.
- Provider secret keys and webhook secrets are strings passed to constructors
  by the host and are never logged, returned or included in an error.
- Fixtures use the provider's test mode and obviously fake identifiers.

## Testing

- Core: unit tests for startup checks, free mode and the grants rules, with a
  table for each subscription status. Catalog validation, plan resolution and
  limits are tested in the pricing spec.
- Stripe adapter: an HTTP test server standing in for the API, recorded event
  payloads for each event type, and signature tests.
- Lifecycle: the conformance suite over the in-memory store and sink, covering
  replays, out-of-order events and unknown prices.
- Live tests against the provider's test mode are opt-in, read the key from the
  environment, and never run by default.

## Findings from the inventory

The [inventory](INVENTORY.md) of the two existing integrations changes this
draft in these ways, which the design items fold in:

- The marketplace fee is in **basis points**, and a seller-set product has a
  **minimum amount**.
- A subscription grants its plan while `past_due`; the two integrations
  disagreed and this follows the one that has run.
- Lifecycle events are ordered by **re-reading the subscription** (or a
  recency check) as well as deduplicated by event ID, because replay-safe state
  writes alone let a late event undo a cancellation.
- The sink is told **where a change came from**, so a host can keep a plan it
  set by hand.
- Legacy host-specific **metadata keys are read as aliases** and the keys
  written use a configurable prefix, so existing Stripe objects keep working.
- A configured provider **must have its webhook secret and prices** at
  startup, and **several webhook secrets** are supported for Connect.

## Open questions

- Whether the spec is YAML, JSON or both on disk, and how a host layers an
  environment override such as a test-mode price.
- How a host counts a period-limited allowance (per month) and where that count
  lives; the library defines the limit and the check, the host supplies the
  count.
- Whether seats and usage-based (metered) prices belong in the first release or
  follow it.
- How a platform that hosts many products represents "one spec per product"
  in one deployment.
- Whether tax, coupons and trials beyond a trial length are in scope.
