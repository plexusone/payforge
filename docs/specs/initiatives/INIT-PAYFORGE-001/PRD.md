# payforge: Product Requirements

**Initiative:** `INIT-PAYFORGE-001`
**Repository:** `github.com/plexusone/payforge`

## Problem

A product that charges for itself needs the same three things, and builds
them again each time:

- **Packaging:** what each plan includes. The price, the features turned on,
  the limits (published items, seats, viewers), and which tier an
  organization lands in when it pays. Usually this lives in Go code beside a
  price in the payment provider's dashboard, and a note to keep the two in step.
- **Billing:** Checkout for a subscription or a one-time purchase, a customer
  portal to change or cancel, marketplace payouts to sellers, and webhooks
  that must be verified, deduplicated and understood.
- **Entitlement:** turning "this subscription is active" into "this
  organization may do these things", including what happens when a payment is
  late, a plan is cancelled, or a limit is reached.

Today these are written per product against Stripe's API, in slightly
different ways: different SDK versions, different metadata conventions,
different ideas of which subscription states keep a plan. A price change is
an edit to code and to the dashboard. There is no shared description of a
plan that a pricing page, an enforcement check and a revenue report can all
read.

## Goals

1. **One description of pricing and packaging.** A declarative, validated
   catalog of plans and products, with features and limits per tier, that
   a product keeps in a file, that a pricing page renders and that
   enforcement reads.
2. **The catalog is the source of truth for the payment provider too.**
   Prices are created and updated in the provider from the catalog and found
   again by a stable key, so changing a price is a catalog change, not a code
   change and a dashboard change that can drift.
3. **Billing lifecycle with Stripe first.** Checkout for subscriptions and
   one-time purchases, the customer portal, Connect accounts and payouts for
   marketplace sales, and webhooks verified and reduced to a small set of
   events the host can act on.
4. **From payment to entitlement.** A paid subscription becomes a tier and its
   limits through a port the host supplies. A plan survives a late payment as
   the provider retries it, and ends when the subscription does.
5. **Enforcement helpers.** Check a feature or a limit against a plan, with
   soft limits that warn and hard limits that refuse.
6. **Several products, one or many provider accounts.** Everything the library
   writes to the provider is tagged with the product it belongs to, so products
   can share one account and still be told apart, or each have their own.
7. **Headless and host-neutral.** Storage, entitlement, authorization and
   audit are interfaces; the library imports no application framework.

## Non-goals

- **Not a payment processor and not a Stripe SDK wrapper.** It uses the
  provider's API for the parts above; it does not expose all of it.
- **Not a revenue reporting tool.** Reading revenue, recurring revenue and
  churn out of a provider account is a separate concern. payforge's job is to
  tag what it creates so such a report can tell products apart.
- **Not tax, invoicing or accounting.** The provider's own tax and invoicing
  features apply.
- **Not a pricing policy.** Which plans exist, who may pay, and what is never
  for sale are decisions of the host's catalog and its own rules.
- **Not a user interface.** It supplies the data a pricing page and a billing
  page need; hosts render them.

## Users

- **Products that sell subscriptions or one-time items,** each with a catalog
  file and a small amount of glue.
- **A platform or application framework** that turns subscriptions into
  organization entitlements and shows pricing and billing screens for the
  products on it.
- **Operators** who change prices, which should be a catalog edit and a
  command.

## Requirements

### Functional

- Load a **catalog**: plans (recurring, with intervals and prices), one-time
  products, features, limits, entitlement tier, trial length and whether a
  plan is self-serve; validate it and publish its JSON Schema.
- **Sync** the catalog to a Stripe account: create or update products and
  prices, keyed by a stable lookup key, never deleting or repricing an
  existing price in place.
- Create **Checkout** sessions for a plan and for a one-time purchase, a
  **customer portal** session, and customers keyed to the host's organization.
- Create and read **Connect** accounts, onboarding links and destination
  charges with a platform fee, for marketplace sales.
- Verify a **webhook** signature and return normalized events: checkout
  completed, subscription changed or ended, invoice paid or failed, connect
  account updated, charge refunded. Processing is idempotent by event ID.
- Apply a subscription event to an **entitlement** through the host's port:
  tier, limits, period end, and whether the plan is currently granted.
- **Check** a feature or limit for a plan, with soft and hard limits.
- Tag every object written to the provider with the product it belongs to.

### Quality

- **Library first.** A small core with no provider dependency; the Stripe
  adapter in its own package; no server, database or CLI in the core.
- **Public-ready.** No host-specific facts, customer data or secrets in code,
  fixtures or history. Fixtures use the provider's test mode and obviously
  fake identifiers.
- **Tested without the network.** The adapter is tested against an HTTP test
  server and recorded event payloads; live tests against the provider's test
  mode are opt-in.
- **Safe defaults.** An unknown plan grants the free tier, never more.

## Success measures

- A product's pricing page, its enforcement checks and its Stripe prices are
  all produced from one catalog file.
- Changing a price is a catalog edit and one sync command.
- A second and third product adopt the library without copying webhook or
  entitlement code.
- A late payment does not remove a plan, a cancelled subscription does, and a
  replayed webhook changes nothing.

## Risks

- **Two existing implementations disagree.** Each of two products already has
  a Stripe integration. The library is extracted from the more complete one,
  and the other is replaced, so the migration must not change behavior that
  customers rely on.
- **Provider API drift.** The Stripe SDK changes versions often. The adapter
  isolates it and reads only a few stable fields from events.
- **Money and policy.** A product may have rules about who may pay or what may
  be sold (for example, a payer who is not the user). Those stay in the host;
  the catalog can describe a plan but cannot decide who is allowed to buy it.
- **Premature generality.** Packaging for subscriptions, one-time items and
  marketplace sales is three shapes. The catalog covers all three but starts
  with the two a host already needs.
