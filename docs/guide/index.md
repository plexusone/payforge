# payforge user guide

payforge is the billing runtime for a product whose pricing and packaging is
described by a [pricing spec](https://github.com/plexusone/systemspec-pricingpackaging).
The spec says what is offered. payforge does what that needs at the payment
provider, which is Stripe today:

1. [Catalog](catalog.md): the spec becomes products and prices in your Stripe
   account, found again by stable lookup keys.
2. [Checkout and the portal](checkout.md): a buyer is sent to a hosted page to
   subscribe or to buy something once, and to another to manage their
   subscription.
3. [Webhooks](webhooks.md): Stripe's events are verified and reduced to a small
   set your code can rely on.
4. [The lifecycle](lifecycle.md): those events become an entitlement for the
   account, through a store and a sink you write.
5. [Marketplaces](marketplace.md): sellers with their own Stripe accounts, sales
   with a platform fee, refunds.

Runnable versions of the main pieces are in [`examples/`](../../examples).

## What payforge does not do

- It does not store anything. Your code keeps accounts, plans and processed
  events; payforge calls it through [ports](lifecycle.md#the-ports) you supply.
- It does not enforce limits. The spec answers "may this account add one more
  board?"; see [`examples/limits`](../../examples/limits).
- It does not talk to any provider but Stripe, though the core package knows
  nothing about Stripe and a second adapter could implement the same interfaces.

## Packages

| Package | Holds |
|---------|-------|
| `github.com/plexusone/payforge` | The provider-neutral core: `Billing`, `Connect`, events, `Lifecycle`, the ports and in-memory versions of them |
| `github.com/plexusone/payforge/stripe` | The Stripe adapter: `Client`, `Webhook`, catalog sync |
| `github.com/plexusone/payforge/payforgetest` | Conformance suites to run against your own store and sinks |

## A first run

Install and check a spec, with no Stripe account:

```bash
go run ./examples/catalog -spec examples/catalog/pricing.json
```

It prints the plans and products and says it is in free mode. Set
`STRIPE_SECRET_KEY` to a **test-mode** key and the same command shows what
syncing would create, without writing anything.

## Free mode

A host with no Stripe key is in free mode: every account is on the spec's
default plan, and every operation that needs Stripe returns
`payforge.ErrNotConfigured`. Use it for development and for deployments that do
not sell anything:

```go
var billing payforge.Billing = payforge.Disabled(spec)
if key != "" {
    billing, err = stripe.New(spec, stripe.Config{SecretKey: key})
    if err != nil {
        return err
    }
}
```

Check the configuration once at startup, so a half-configured deployment stops
instead of showing plans that never change:

```go
err := payforge.CheckStartup(spec, payforge.ProviderConfig{
    SecretKey:      key,
    WebhookSecrets: []string{platformSecret, connectSecret},
})
```

With no key it succeeds. With a key it requires a valid spec, something to sell,
and a webhook secret: two when the spec has a marketplace or a seller-set
product, because events from sellers' accounts arrive on their own endpoint.
Errors name what is missing and never contain a secret.
