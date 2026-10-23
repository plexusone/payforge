# payforge

The billing runtime for a product whose pricing and packaging is described by a
[pricing spec](https://github.com/plexusone/systemspec-pricingpackaging). The
spec says what is offered; payforge does what that needs at the payment
provider, Stripe today: it computes the products and prices the provider should
hold, starts checkout and the customer portal, verifies webhooks, and turns a
paid subscription into an entitlement through ports the host supplies.

- [Overview](guide/index.md): what it does and does not do, the packages, free mode.
- [The catalog](guide/catalog.md): the spec as products and prices, found by lookup keys, and syncing.
- [Checkout and the portal](guide/checkout.md): subscribing, buying once, managing a subscription.
- [Webhooks](guide/webhooks.md): verifying Stripe's events and what they carry.
- [The lifecycle](guide/lifecycle.md): events to entitlements, and writing a store and a sink.
- [Marketplaces](guide/marketplace.md): sellers, sales with a platform fee, refunds.
- [Known limitations](guide/limitations.md): what is not covered and what has not yet run against real Stripe.

Runnable [examples](https://github.com/plexusone/payforge/tree/main/examples)
cover a catalog sync, a pricing page, a webhook endpoint and limit checks.

```bash
go get github.com/plexusone/payforge
```
