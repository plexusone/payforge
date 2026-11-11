# Releases

Release notes, one file per version, newest first. The
[changelog](changelog.md) is generated from `CHANGELOG.json` and lists every
change with its commit.

The latest release, [v0.1.1](v0.1.1.md), treats names under `localhost` as
loopback hosts: a test-mode key accepts them and a live key refuses them,
with or without a trailing dot.

[v0.1.0](v0.1.0.md) is the first release: a billing runtime for a product
described by a pricing spec, with startup checks, a free mode, a Stripe
adapter for checkout, the portal, Connect sales, catalog sync and webhooks, and
a lifecycle that turns provider state into entitlements.
