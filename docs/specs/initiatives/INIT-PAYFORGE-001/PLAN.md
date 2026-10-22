# payforge: Plan

**Initiative:** `INIT-PAYFORGE-001`
**Repository:** `github.com/plexusone/payforge`

Implements the [PRD](PRD.md) and [TRD](TRD.md). The [roadmap](ROADMAP.md)
holds the work items and their status.

## Approach

- **Extract, don't invent.** Two products already integrate with Stripe. The
  review of both comes first, and the library is extracted from the more
  complete one, so it starts with behavior that has run rather than a guess.
- **The catalog first.** Packaging is the part every product shares and the
  part nobody has written down as data. It is specified, validated and given
  a JSON Schema before the Stripe code is moved.
- **Provider-neutral core, Stripe in an adapter.** Limits, plan resolution and
  the entitlement lifecycle are tested without any payment SDK.
- **Sync closes the drift.** Prices are created from the catalog and found by
  lookup key, so a price change is one edit and one command.
- **Prove it on one host before releasing.** The product the code is extracted
  from adopts the library first; the others and a platform follow in their own
  repositories.
- **Public-ready from the first commit.** No host-specific facts, no customer
  data, test-mode fixtures only.

## Phases

| Phase | Theme | Outcome |
|---|---|---|
| 1 | Review and design | Both existing integrations inventoried, the catalog and lifecycle designed, and the TRD frozen |
| 2 | Catalog and limits | The catalog types, loader, validation, JSON Schema, plan resolution and limit checks |
| 3 | Stripe adapter | Checkout, portal, customers, Connect, webhooks and catalog sync, extracted and tested |
| 4 | Lifecycle | Subscription events become entitlements through ports, idempotently, with in-memory implementations |
| 5 | Adoption and release | The source product adopts it, documentation, examples and v0.1.0 |

## Downstream work, in other repositories

These follow payforge's release and are planned in the repositories that own
them, each with its own roadmap items:

- A **platform** adopts payforge for its organization subscriptions, so a paid
  plan becomes the organization's entitlement tier, and a **web kit** renders
  the pricing and billing screens from the catalog.
- Each **product** writes a catalog that conforms to the format and replaces
  any billing code of its own.
- A **multi-product dashboard** reads each product's revenue from its payment
  account, grouped by the `payforge_app` tag.

## Order and dependencies

Phase 1 gates the rest. Phase 2 and the part of Phase 3 that does not touch the
catalog can overlap. Sync depends on the catalog. The lifecycle depends on the
catalog and on normalized events. Phase 5 follows all of them.

## Verification

- Catalog and limits: table tests, including every mistake the loader rejects.
- Stripe adapter: an HTTP test server and recorded payloads; signature tests.
- Lifecycle: the conformance suite, with replayed and out-of-order events.
- The source product runs its existing tests unchanged after adopting the
  library, which is the check that migration preserved its behavior.
