# payforge

[![Go CI][go-ci-svg]][go-ci-url]
[![Go Lint][go-lint-svg]][go-lint-url]
[![License][license-svg]][license-url]

 [go-ci-svg]: https://github.com/plexusone/payforge/actions/workflows/go-ci.yaml/badge.svg?branch=main
 [go-ci-url]: https://github.com/plexusone/payforge/actions/workflows/go-ci.yaml
 [go-lint-svg]: https://github.com/plexusone/payforge/actions/workflows/go-lint.yaml/badge.svg?branch=main
 [go-lint-url]: https://github.com/plexusone/payforge/actions/workflows/go-lint.yaml
 [license-svg]: https://img.shields.io/badge/license-MIT-blue.svg
 [license-url]: https://github.com/plexusone/payforge/blob/main/LICENSE

The billing runtime for a product whose pricing and packaging is described by a
[pricing spec](https://github.com/plexusone/systemspec-pricingpackaging). The
spec says what is offered; payforge does what that needs at the payment
provider: it computes the products and prices the provider should hold, starts
checkout and the customer portal, and turns a paid subscription into an
entitlement through ports the host supplies.

> **Status:** in development. The provider-neutral core below exists. Checkout,
> the Stripe adapter, webhooks and the subscription lifecycle follow; see the
> [roadmap](docs/specs/initiatives/INIT-PAYFORGE-001/ROADMAP.md).

```go
spec, err := pricingspec.Load(f)
if err != nil { return err }

// Stop at startup if billing is half configured or cannot sell the spec.
if err := payforge.CheckStartup(spec, payforge.ProviderConfig{
	SecretKey: key, WebhookSecrets: []string{endpointSecret},
}); err != nil {
	return err
}

// With no key, every account is on the spec's default plan.
var billing payforge.Billing = payforge.Disabled(spec)

// What a provider account should hold, found again by stable lookup keys.
plan := payforge.ProviderPlanFor(spec)
```

Everything written to the provider carries `payforge_app`, so several products
can share one account and still be told apart.

## Development

```bash
go test ./...
go test . -update   # rewrite the golden files, then review the diff
```
