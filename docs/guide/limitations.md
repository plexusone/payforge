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

An opt-in live test exists for these (`stripe/live_test.go`) and has **not been
run yet**. See [Running the live test](#running-the-live-test).

## Design limits

- **Locking is the host's.** `Lifecycle.Handle` and plan checkout serialize per
  account only through a `Locker` the host supplies (`Lifecycle.Locker`,
  `stripe.Config.Locker`); `MemoryLocker` covers a single process. Without one,
  events for one account handled at once can apply in either order, and two plan
  checkouts made at the same moment can both stay open. See
  [the lifecycle](lifecycle.md#subscriptions-are-decided-from-stripe-not-from-the-event).
- **One open plan checkout per subject.** A new plan checkout expires the
  subject's other open plan checkouts for the app, so an abandoned tab cannot be
  paid later. One-time purchases are not limited.
- **One Stripe API call per subscription event, and per sale**, to read the
  provider's current state. That is the price of order not mattering.
- **Cross-border sellers** are not supported; see
  [marketplaces](marketplace.md#sellers-outside-your-stripe-region).
- **A changed customer email inside a day** can create a second customer when
  Search has not caught up, instead of failing.
- **Plan IDs are permanent.** Renaming one creates a new product and leaves the
  old one; see [the catalog](catalog.md#changing-the-catalog).
- **Fixed-price sale fees come from the spec**, not from the price Stripe holds.
- **One-time sales being paid**, and a delayed payment failing
  (`EventCheckoutFailed`), are the host's to record; refunds and disputes are
  applied through their sinks.
- **Disputes name the charge, not the buyer**: the host matches a
  `ChargeDispute` to what it recorded for that charge, and decides whether to
  suspend access or cancel a disputed subscription. `charge.dispute.updated` and
  early fraud warnings are not mapped.
- **Keyless legacy prices** grant nothing unless listed in
  `Lifecycle.LegacyPrices`, each with the plan it is for; see
  [the catalog](catalog.md#accounts-that-already-have-objects). There is no way
  to have such a price named by metadata.
- **In-memory ports** (`MemoryStore`, `MemorySink`) grow without bound and lose
  everything on restart. They are for tests.
- **Stripe only.** The core is provider-neutral; there is no second adapter.

## Running the live test

The live test runs payforge against Stripe's **test mode**. It does nothing unless
you set a test-mode secret key, and it refuses any key that is not `sk_test_...`:

```bash
PAYFORGE_LIVE_STRIPE_KEY=sk_test_... go test ./stripe -run Live -v
```

It uses an app name made up for the run, so it never touches objects it did not
create, and it archives or deletes what it made when it ends (anything it could
not remove is logged). It never runs in CI, because the variable is not set there.

It checks, against the real API:

- Syncing an empty account, repeating as unchanged, and a price amount that goes
  from A to B and back to A the same day.
- Creating a customer, and a second call with a changed email.
- A plan checkout: a hosted session carrying our metadata, and a repeat that
  expires the first.
- A real subscription (made with Stripe's test card): reading it back, refusing a
  second checkout, and sending the events Stripe made for it through the webhook
  reader and the lifecycle, signed here with a secret of the test's own.
- Connect: creating an Express account, a changed email, an onboarding link, and
  refusing a sale by a seller who has not onboarded. This part is skipped on an
  account that has not enabled Connect.

Lines that begin with `OBSERVED` record behavior it does not assert, such as
whether Customer Search had caught up. Not covered: the deleted-product case,
refunds, and a sale that completes, which need a browser or a funded seller.
