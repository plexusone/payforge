# payforge: Inventory of the Existing Integrations

**Initiative:** `INIT-PAYFORGE-001`
**Work item:** `RMI-PAYFORGE-001`

Two integrations with Stripe exist today, and payforge replaces both. This
inventory records what each does, what customers can see and a migration must
keep, and the defects that must not be carried over. It was made by reading the
code; nothing was run against Stripe. The two are called **A** and **B** here:

- **A** is the billing of a publishing platform: sellers subscribe to plans, and
  buyers purchase items from sellers.
- **B** is the marketplace in an application framework: organizations buy
  licenses to listings.

## Summary

| | A (publishing platform) | B (framework marketplace) |
|---|---|---|
| Stripe SDK | v87 | v84, key set as a package global |
| What is sold | Plan subscriptions on the platform's account; item sales paid to each seller | A license per listing: one-time, subscription or per-seat |
| Prices | Stripe Price IDs from configuration, one per self-serve plan | Inline `price_data` from the listing; no Stripe product or price objects |
| Customers | One per organization, created idempotently, used for checkout and the portal | None; Stripe makes a guest customer |
| Customer portal | Yes | No |
| Connect | Express accounts, onboarding, destination charges with `on_behalf_of` and a platform fee | Express accounts created; never attached to a charge; payout and balance are stubs |
| Subscription becomes an entitlement | Yes: the organization's plan is set from subscription events | No: the handlers for subscription updates, invoices and failures are empty |
| Statuses that keep access | `active`, `trialing`, `past_due` | Licenses: existence and a time window; subscriptions: `active`, `trialing` only |
| Event deduplication | State-based only; event IDs are not stored | State-based only; event IDs are not stored |
| Idempotency keys | On customers, accounts and item checkout | None |
| Plan catalog with features and limits | Yes, in code | None; the tier is a free string |
| Where it runs | A mounted route and database tables | A library no host mounts; the framework's entitlement source is built with no subscriptions |

The two disagree on the one rule that matters most for a subscription: whether
a payment that is late (`past_due`) keeps the plan. A keeps it while Stripe
retries; B's subscription check does not. payforge follows A.

## A: the publishing platform

### What it exposes

- Connect: read an organization's account state, and start onboarding. Only
  owners and admins; other callers get a 403.
- Item checkout for any signed-in user, for a published, paid item priced
  at least 50 cents, sold by an organization whose plan allows selling and
  whose Stripe account can take charges. A pending purchase is recorded with
  the platform fee computed up front.
- Billing: the plan, the catalog, status, period end and the number of
  published items; start a plan checkout (a self-serve plan); open the
  customer portal.
- A webhook, verified by signature, with the body limited to 1 MiB.

### How it uses Stripe

- Item sale: Checkout in payment mode as a **destination charge** with
  `on_behalf_of` and `transfer_data.destination` set to the seller's account,
  so the seller is the merchant on the statement, plus an application fee
  (the platform fee in basis points of the amount, rounded down) when above
  zero. Metadata is set on the session and the payment intent.
- Plan: Checkout in subscription mode for a configured Price ID with the
  organization's customer, metadata on the session **and** the subscription.
- Customers and Express accounts are created with idempotency keys.
- The portal returns the customer to the billing page.

### Events and what they change

| Event | Effect |
|---|---|
| `checkout.session.completed`, `async_payment_succeeded` | Only for an item purchase: if paid, mark the purchase paid and grant the buyer access. Plan checkouts are deliberately ignored; the subscription events apply them. |
| `customer.subscription.created`, `updated`, `deleted` | Find the organization (metadata, then customer), find the plan from the first line item's price, set the organization's plan and upsert one subscription row. |
| `account.updated` | Update the seller's charges, payouts and details flags. |
| `charge.refunded` | Only a **full** refund: mark the purchase refunded and remove the buyer's access, keeping their data. |

- A subscription **grants** its plan while `active`, `trialing` or `past_due`.
  Every other status, and a deleted subscription, returns the organization to
  `free`.
- A price the catalog does not know never grants a plan; it is logged.
- A stale-event guard ignores a non-granting event for a different
  subscription than the stored live one.

### Plans

| Plan | Price | Published items | Sell items | Custom domain | Self-serve |
|---|---|---|---|---|---|
| free | 0 | a small number | no | no | no |
| lower paid | monthly | unlimited | yes | no | yes |
| higher paid | monthly | unlimited | yes | yes | yes |
| enterprise | arranged | unlimited | yes | yes | no |

Limits are checked at the moment of the action and nowhere else: creating or
changing a paid item, publishing, taking an item checkout, and setting or
serving a custom domain. A downgrade does not unpublish anything; it blocks the
next publish, stops new sales and stops serving the custom domain. Existing
buyers keep their access. An unknown plan value falls back to free.

### Customer-visible behavior a migration must keep

- Status codes and messages: **402** for a plan limit ("The Free plan cannot
  sell items…", "The Free plan allows N published item(s)…", the custom
  domain message) and for an unpaid paid item; **409** for "not for sale",
  "not on sale right now", "already has access", "already has a plan", "no billing
  account yet"; **422** for a plan that cannot be bought here or a price under
  the floor; **502** retry messages when Stripe fails; **503** when payments
  are not configured.
- Redirects after checkout, the portal and onboarding, and the
  `session_id` placeholder on return URLs.
- The destination charge shape and the fee in basis points.
- Past due keeps the plan; an unknown price never grants one.
- A full refund removes the buyer's access and keeps their data, and buying
  again restores it.
- **Existing Stripe objects carry host-specific metadata keys**
  (`<host>_kind`, `<host>_organization_id`, `<host>_plan`, `<host>_purchase_id`,
  `<host>_item_id`, `<host>_principal_id`) and customer and account metadata.
  After migration these
  objects still arrive in webhooks, so payforge must be able to **read legacy
  metadata keys** as aliases while writing its own.
- The idempotency key prefixes `item-checkout-`, `customer-`,
  `connect-account-`.

### Configuration

A secret key (the master switch), a webhook secret, one Price ID per
self-serve plan, a platform fee in basis points (0 to 10000), and the frontend
URL for return addresses. With no secret key set, **every organization is
treated as Enterprise**: nothing is limited, and nothing can be sold.

## B: the framework marketplace

### Model

- A **listing** has a pricing model (free, one-time, subscription, per-seat),
  a price in minor units and a currency, and grants an application.
- A **license** ties an organization to a listing with seats, a validity window
  and a Stripe subscription ID. There is no status; revoking deletes it.
- A **subscription** is a record with a status, a plan tier string and period
  dates. An organization has at most one platform-wide subscription and may
  have one per listing.
- Entitlements are derived from active subscriptions, valid licenses and
  default applications. They drive which application roles can be assigned.
  The tier and seats are not in token claims.

### What it does with Stripe

- Checkout with inline price data from the stored listing (the price is never
  taken from the client), the listing's currency, a fixed monthly interval, and
  a seat quantity from the client.
- Metadata on the session: the listing, organization, purchaser and seats.
- On a completed, paid checkout: grant a license (perpetual, even for a
  subscription). On a deleted subscription: revoke the license.
- Express accounts, onboarding links, payouts and a balance placeholder, none
  connected to a charge or persisted.

### What is missing or not wired

- No webhook route exists; the verifying function has no caller.
- The handlers for `customer.subscription.created` and `updated`,
  `invoice.paid` and `invoice.payment_failed` return nil, so renewals, failures
  and status changes are never applied.
- The platform subscription is never created or updated from Stripe.
- The framework builds its entitlement source with no subscriptions, licenses
  or listings, so nothing bought through Stripe reaches a role.
- No plan catalog, no features or limits per tier, no pricing page, no
  customers, no portal, no trials, no Stripe products or prices.

## Defects not to carry over

From A:

1. **No ordering protection.** Event IDs are not stored and events are not
   ordered. A late `updated` with status `active` after `deleted` for the same
   subscription grants the plan again. Fix: re-read the subscription from
   Stripe, or record the last applied event time, before applying.
2. **A manually set plan is overwritten** by the next subscription event for
   that organization.
3. **One webhook secret.** Connect events can arrive on a second endpoint with
   its own signing secret; one configured secret rejects them.
4. **A missing webhook secret or price ID is not an error at startup.** Limits
   then apply while plans can never change.
5. **Stripe off means Enterprise for everyone,** and switching it on applies
   free limits to every organization at once with no grace.
6. **Only the first line item** of a subscription is read.
7. **A refund can arrive before the payment** is recorded and is then lost; a
   partial refund and disputes are ignored; a buyer can pay twice because the
   access check and the purchase are not atomic; pending purchases are never
   expired.
8. **The period end falls back to "now"** when an event lacks it, and the
   stored tier of a cancelled subscription is misleading.
9. **No idempotency key on plan checkout,** so two tabs can create two
   subscriptions for one customer.
10. **Prices are written twice:** the display price in code and the Price ID in
    Stripe, kept in step by hand.

From B:

1. The stubs above, and the missing webhook route.
2. A recurring per-seat price created in payment mode, which Stripe rejects.
3. A perpetual license for a subscription; a repeat purchase or seat increase
   is silently dropped.
4. No idempotency keys, no event deduplication, and a webhook error for a
   deleted listing that Stripe will retry forever.
5. A package-global API key and calls that ignore the request context.
6. Unvalidated return URLs and an unbounded seat count from the client.

## What the catalog and the lifecycle must therefore express

- **Plans** with a tier, a price per interval, features, **counted limits** and
  a self-serve flag; a plan that is arranged, not bought (enterprise).
- **Seller-set prices** for one-time sales, with a **minimum amount** and a
  currency, not only fixed prices.
- **A platform fee in basis points,** not whole percent.
- **Grants while `past_due`,** and a documented rule for every other status.
- **Unknown price never grants,** and an unknown plan value resolves to free.
- **Legacy metadata keys** read as aliases, and a configurable prefix for the
  keys payforge writes.
- **Ordering safety** through a re-read or a recency check, plus event-ID
  deduplication.
- **A host-managed override:** the lifecycle tells the sink where the change
  came from, so a host can keep a plan it set by hand.
- **Startup validation:** a configured provider needs its webhook secret and the
  catalog's prices present; a missing piece is an error, not a silent mode.
- **Idempotency keys** on every creating call, and the request context passed
  through.
- **Several webhook secrets,** for the account and Connect endpoints.

## Behavior the host owns

These stay in each product and are not payforge's: granting and removing
access on a refund, the wording of its messages, which plan checks run where,
who may buy (for example a payer who is not the user), and what a
product refuses to sell.
