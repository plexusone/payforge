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
payforge            catalog · limits · lifecycle · ports
      │      (provider-neutral core: no payment SDK)
      ▼
payforge/stripe     checkout · portal · connect · webhooks · sync
      │
      ▼
Stripe API
```

The core never imports a payment SDK. The Stripe adapter is the only package
that does, so a host that only reads a catalog and checks limits does not build
the Stripe client.

## The catalog

A catalog is the declarative description of one product's pricing and
packaging. It is a document (YAML or JSON) loaded into Go types; the Go types
are the source of truth and the published JSON Schema is generated from them.

```yaml
formatVersion: "1"
app: example-app            # tags everything written to the provider
currency: usd

features:                   # what a plan can switch on or off
  custom_domain: { description: "Serve on the customer's own domain" }
  sell_items:    { description: "Sell items to others" }

limits:                     # what a plan can cap
  published_items: { unit: item, description: "Published items" }

plans:
  - id: free
    name: Free
    tier: free
    selfServe: true
    limits: { published_items: { max: 1 } }
  - id: pro
    name: Pro
    tier: pro
    selfServe: true
    trialDays: 14
    prices:
      - { interval: month, amountCents: 2000 }
      - { interval: year,  amountCents: 20000 }
    features: [sell_items, custom_domain]
    limits: { published_items: { max: null } }   # null: unlimited
  - id: enterprise
    name: Enterprise
    tier: enterprise
    selfServe: false         # arranged with the seller; no checkout
    features: [sell_items, custom_domain]

products:                    # one-time purchases
  - id: verified-record
    name: Verified record
    amountCents: 999

marketplace:                 # sales by sellers, paid out through Connect
  platformFee: { percent: 10 }
```

Rules the loader enforces:

- Plan IDs, feature keys, limit keys and product IDs are unique; plans and
  products refer only to declared features and limits.
- Every plan has exactly one `tier`; tiers are an open set of strings the host
  maps to entitlements. A plan with prices is recurring and a plan without is
  not sold through checkout.
- A limit's `max` is a number or `null` for unlimited. Zero means zero, not
  unlimited, so a mistake never grants more than intended.
- Amounts are integers in minor units. A plan has at most one price per
  interval.
- An unknown plan ID resolves to the free tier (or to the plan the catalog
  names as `default`), so a stale or unexpected value never grants more.
- A soft limit (`soft: true`) warns and does not refuse.

The catalog does not hold provider identifiers. Prices are found in the
provider by lookup key (below), so the same file works against test and live
accounts.

### Reading the catalog

```go
cat, err := payforge.LoadCatalog(r)       // validates
plan := cat.PlanOrDefault("pro")
d := plan.Check(payforge.LimitPublishedItems, current)  // Allowed, Warn, Refuse
ok := plan.Has("custom_domain")
```

A pricing page renders `cat.Plans`; enforcement calls `Check` and `Has`.

## Provider sync

`stripe.Sync(ctx, client, cat)` makes a Stripe account match the catalog:

- A product per plan and per one-time product, with metadata
  `payforge_app=<app>` and `payforge_plan=<id>`.
- A price per interval, with the **lookup key**
  `<app>/<plan>/<interval>` (and `<app>/<product>` for one-time products).
- A price is immutable in Stripe, so a changed amount creates a new price,
  moves the lookup key to it and leaves the old one in place for existing
  subscribers. Sync never deletes and never edits an amount.
- Sync is idempotent and has a dry-run mode that reports what it would do.

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
- Return URLs are supplied by the host and carry the session ID.

## Connect (marketplace)

```go
type Connect interface {
    CreateAccount(ctx, Subject) (accountID string, err error)
    OnboardingLink(ctx, accountID, refreshURL, returnURL string) (string, error)
    Account(ctx, accountID string) (Account, error)       // charges, payouts, details
    CheckoutSale(ctx, Sale) (Session, error)              // destination charge
}
```

A sale is a destination charge on behalf of the seller's account: the seller
is the merchant on the statement, and the platform fee is taken as an
application fee. Express accounts, because the provider hosts onboarding.

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
- A subscription **grants** its plan while `active`, `trialing` or
  `past_due` (the provider is retrying a payment), and stops granting when it
  is ended or `unpaid` or `canceled`.

## Lifecycle and ports

```go
type EntitlementSink interface {
    Apply(ctx context.Context, e Entitlement) error   // tier, limits, validUntil, granted
}

type Store interface {                  // the host's persistence
    Subject(ctx, customerID string) (Subject, error)
    Seen(ctx, eventID string) (bool, error)
    MarkSeen(ctx, eventID string) error
}

type Lifecycle struct { Catalog *Catalog; Store Store; Sink EntitlementSink }

func (l *Lifecycle) Handle(ctx context.Context, ev Event) error
```

- `Handle` resolves the subject (from metadata, then from the customer),
  resolves the plan (from the price's lookup key, falling back to metadata),
  and tells the sink the resulting entitlement: the plan's tier, limits and
  features, the period end, and whether it is granted.
- It is idempotent: an event already seen changes nothing, and events that
  arrive out of order for one subscription use the provider's own
  subscription state, not the order received.
- A subscription for a price the catalog does not know is logged and
  ignored, never granted.
- The host implements `EntitlementSink` to update its own record of the
  organization's plan; a platform implements it by writing the organization's
  entitlement.

Persistence, authorization and audit are interfaces the host supplies. The
library includes in-memory implementations and a conformance suite for them.

## Metadata and revenue reporting

Everything written to the provider carries `payforge_app`, and subscriptions
and sales also carry the subject and plan. Several products can therefore
share one account, or each have their own; a report that reads revenue out of
the account can group by `payforge_app` and `payforge_plan`. payforge does not
read revenue.

## Errors and secrets

- Sentinel errors for the cases a host handles: not configured, unknown plan,
  not self-serve, signature invalid, event already processed.
- Provider secret keys and webhook secrets are strings passed to constructors
  by the host and are never logged, returned or included in an error.
- Fixtures use the provider's test mode and obviously fake identifiers.

## Testing

- Core: unit tests for catalog validation, plan resolution, limits and the
  grants rules, with a table for each subscription status.
- Stripe adapter: an HTTP test server standing in for the API, recorded event
  payloads for each event type, and signature tests.
- Lifecycle: the conformance suite over the in-memory store and sink, covering
  replays, out-of-order events and unknown prices.
- Live tests against the provider's test mode are opt-in, read the key from the
  environment, and never run by default.

## Open questions

- Whether the catalog is YAML, JSON or both on disk, and how a host layers an
  environment override such as a test-mode price.
- Whether seats and usage-based (metered) prices belong in the first release or
  follow it.
- How a platform that hosts many products represents "one catalog per product"
  in one deployment.
- Which of the two existing integrations' behaviors are customer-visible and
  must be preserved on migration (to be listed during the review item).
- Whether tax, coupons and trials beyond a trial length are in scope.
