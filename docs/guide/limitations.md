# Known limitations

What payforge does not do, and what has not been proven.

## Not yet run against real Stripe

Every test uses a stand-in for the Stripe API or hand-written payloads. The
stand-in enforces idempotency keys as Stripe documents them, but it is still our
reading of Stripe. These have not been confirmed against Stripe's test mode:

- That a key reused with different parameters is refused, and a key replays its
  response for a day.
- The shape of a deleted product when read, and that `active` is false for it.
- That Checkout accepts `on_behalf_of` with a platform-owned customer, and refuses
  an application fee larger than the amount.
- Real event payloads under an account's pinned API version, for subscriptions,
  invoices, charges and accounts.
- That a charge from a Checkout payment inherits the payment intent's metadata.
- That a trial that ends without a card emits `customer.subscription.deleted`.
- How long Customer Search lags a creation.
- That `transfer_lookup_key` moves a key off an archived price.

An opt-in live test against test mode is on the roadmap and will settle these.

## Design limits

- **No locking.** `Lifecycle.Handle` and checkout do not serialize. Hold a lock
  per account when events can be handled at once, and per subject around checkout
  if a second subscription must never be possible. See
  [the lifecycle](lifecycle.md#subscriptions-are-decided-from-stripe-not-from-the-event).
- **One Stripe API call per subscription event, and per sale**, to read the
  provider's current state. That is the price of order not mattering.
- **Cross-border sellers** are not supported; see
  [marketplaces](marketplace.md#sellers-outside-your-stripe-region).
- **A changed customer email inside a day** can create a second customer when
  Search has not caught up, instead of failing.
- **Plan IDs are permanent.** Renaming one creates a new product and leaves the
  old one; see [the catalog](catalog.md#changing-the-catalog).
- **Fixed-price sale fees come from the spec**, not from the price Stripe holds.
- **One-time sales being paid** and delayed payment methods are the host's to
  record; only refunds are applied.
- **Keyless legacy prices** are trusted through the metadata on the platform's own
  subscriptions. Anyone who can write that metadata can name a self-serve plan for
  a subscription with a keyless price.
- **In-memory ports** (`MemoryStore`, `MemorySink`) grow without bound and lose
  everything on restart. They are for tests.
- **Stripe only.** The core is provider-neutral; there is no second adapter.
