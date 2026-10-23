# ROADMAP — payforge: Pricing, Packaging and Subscription Billing

**Initiative:** `INIT-PAYFORGE-001`
**Repository:** `github.com/plexusone/payforge`

Specs: [PRD](PRD.md), [TRD](TRD.md), [PLAN](PLAN.md). Work items carry stable
IDs (`RMI-PAYFORGE-NNN`), are reviewed and executed by phase, and commits
implementing one carry the trailer `Refs: RMI-PAYFORGE-<NNN>`.

Later, not scheduled: seat-based and metered prices, coupons, tax
configuration, other payment providers, and a command-line tool over the core.

## Phase 1 — Review and design

**Theme:** Both existing Stripe integrations are inventoried, the catalog and the lifecycle are designed from them, and the TRD is frozen before code is moved.

- [x] `RMI-PAYFORGE-001` Inventory the two existing Stripe integrations: what each does, the metadata and states it relies on, and the behavior customers can see
- [ ] `RMI-PAYFORGE-002` Design the catalog format from the plans and packaging in use, covering recurring plans, one-time products and marketplace sales
  - Depends on: `RMI-PAYFORGE-001`
- [ ] `RMI-PAYFORGE-003` Design the lifecycle: events, the subscription states that grant a plan, ports for storage and entitlement, and idempotency
  - Depends on: `RMI-PAYFORGE-001`
- [ ] `RMI-PAYFORGE-004` Freeze the TRD, list the behavior a migration must preserve, and record the open questions that remain
  - Depends on: `RMI-PAYFORGE-002`, `RMI-PAYFORGE-003`

## Phase 2 — Consume the pricing spec

**Theme:** payforge reads a product's pricing and packaging from the pricing spec, runs without a provider in free mode, and fails at startup when billing is configured but the spec cannot support it.

- [x] `RMI-PAYFORGE-005` Module and package layout, importing the pricing spec's types
  - Depends on: `RMI-PAYFORGE-004`, `RMI-SYSTEMSPECPRICINGPACKAGING-004`
- [x] `RMI-PAYFORGE-006` Startup checks: when a provider is configured, every self-serve plan has a price and the webhook secret is set; otherwise an error
  - Depends on: `RMI-PAYFORGE-005`
- [x] `RMI-PAYFORGE-007` Free mode: a billing implementation with no provider whose plan lookups return the spec's default plan and whose checkout calls report not configured
  - Depends on: `RMI-PAYFORGE-005`
- [x] `RMI-PAYFORGE-008` Provider plan: the products and prices a provider should hold, with stable keys, computed from the spec
  - Depends on: `RMI-PAYFORGE-005`

## Phase 3 — Stripe adapter

**Theme:** Checkout, the customer portal, customers, Connect, webhooks and catalog sync are extracted from the existing integration into one tested adapter.

- [x] `RMI-PAYFORGE-009` Stripe client setup, customers keyed to the host's subject, and plan and one-time checkout, with the app tag on everything written
  - Depends on: `RMI-PAYFORGE-006`
- [x] `RMI-PAYFORGE-010` Customer portal sessions
  - Depends on: `RMI-PAYFORGE-009`
- [x] `RMI-PAYFORGE-011` Connect: Express accounts, onboarding links, account state and destination-charge sales with a platform fee
  - Depends on: `RMI-PAYFORGE-009`
- [x] `RMI-PAYFORGE-012` Webhook verification and the normalized event set, reading only stable fields
  - Depends on: `RMI-PAYFORGE-003`
- [x] `RMI-PAYFORGE-013` Catalog sync to a Stripe account: products and prices by lookup key, new price on a changed amount, never deleting, with a dry run
  - Depends on: `RMI-PAYFORGE-006`

## Phase 4 — Lifecycle

**Theme:** A paid subscription becomes the host's entitlement through ports, correctly under replay, reordering and late payments.

- [x] `RMI-PAYFORGE-014` Ports: the host's store and entitlement sink, and in-memory implementations
  - Depends on: `RMI-PAYFORGE-003`
- [x] `RMI-PAYFORGE-015` Lifecycle handler: subject and plan resolution, which subscription states grant a plan, and idempotency by event
  - Depends on: `RMI-PAYFORGE-012`, `RMI-PAYFORGE-014`, `RMI-PAYFORGE-007`
- [x] `RMI-PAYFORGE-016` Conformance suite for stores and sinks, and lifecycle tests for replays, out-of-order events and unknown prices
  - Depends on: `RMI-PAYFORGE-015`
- [x] `RMI-PAYFORGE-017` Marketplace sale events: refunds and Connect account changes applied through sinks the host supplies
  - Depends on: `RMI-PAYFORGE-011`, `RMI-PAYFORGE-015`

## Phase 5 — Adoption and release

**Theme:** The product the code came from runs on the library with unchanged behavior, and the library is documented and released.

- [ ] `RMI-PAYFORGE-018` The source product adopts payforge: its plans become a catalog and its billing code is replaced, with its existing tests passing unchanged
  - Depends on: `RMI-PAYFORGE-016`, `RMI-PAYFORGE-013`
- [ ] `RMI-PAYFORGE-019` User guide: the catalog format, syncing prices, checkout and the portal, webhooks and the lifecycle, and writing a store and a sink
  - Depends on: `RMI-PAYFORGE-016`
- [x] `RMI-PAYFORGE-020` Examples: a catalog, a pricing page, a webhook handler and limit enforcement
  - Depends on: `RMI-PAYFORGE-016`
- [ ] `RMI-PAYFORGE-021` Opt-in live test against Stripe's test mode
  - Depends on: `RMI-PAYFORGE-013`
- [ ] `RMI-PAYFORGE-022` Review of the core and adapter; fix findings; record known limitations; v0.1.0 release with changelog, notes and tag after CI
  - Depends on: `RMI-PAYFORGE-018`, `RMI-PAYFORGE-019`, `RMI-PAYFORGE-020`, `RMI-PAYFORGE-021`
