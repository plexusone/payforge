# PayForge

[![Go CI][go-ci-svg]][go-ci-url]
[![Go Lint][go-lint-svg]][go-lint-url]
[![Go SAST][go-sast-svg]][go-sast-url]
[![Docs][docs-godoc-svg]][docs-godoc-url]
[![DevGuide][docs-mkdoc-svg]][docs-mkdoc-url]
[![Visualization][viz-svg]][viz-url]
[![License][license-svg]][license-url]

 [go-ci-svg]: https://github.com/plexusone/payforge/actions/workflows/go-ci.yaml/badge.svg?branch=main
 [go-ci-url]: https://github.com/plexusone/payforge/actions/workflows/go-ci.yaml
 [go-lint-svg]: https://github.com/plexusone/payforge/actions/workflows/go-lint.yaml/badge.svg?branch=main
 [go-lint-url]: https://github.com/plexusone/payforge/actions/workflows/go-lint.yaml
 [go-sast-svg]: https://github.com/plexusone/payforge/actions/workflows/go-sast-codeql.yaml/badge.svg?branch=main
 [go-sast-url]: https://github.com/plexusone/payforge/actions/workflows/go-sast-codeql.yaml
 [docs-godoc-svg]: https://pkg.go.dev/badge/github.com/plexusone/payforge
 [docs-godoc-url]: https://pkg.go.dev/github.com/plexusone/payforge
 [docs-mkdoc-svg]: https://img.shields.io/badge/Go-dev%20guide-blue.svg
 [docs-mkdoc-url]: https://plexusone.github.io/payforge
 [viz-svg]: https://img.shields.io/badge/visualizaton-Go-blue.svg
 [viz-url]: https://mango-dune-07a8b7110.1.azurestaticapps.net/?repo=plexusone%2Fpayforge
 [loc-svg]: https://tokei.rs/b1/github/plexusone/payforge
 [repo-url]: https://github.com/plexusone/payforge
 [license-svg]: https://img.shields.io/badge/license-MIT-blue.svg
 [license-url]: https://github.com/plexusone/payforge/blob/main/LICENSE

The billing runtime for a product whose pricing and packaging is described by a
[pricing spec](https://github.com/plexusone/systemspec-pricingpackaging). The
spec says what is offered; payforge does what that needs at the payment
provider: it computes the products and prices the provider should hold, starts
checkout and the customer portal, and turns a paid subscription into an
entitlement through ports the host supplies.

> **Status:** in development. The provider-neutral core and the Stripe
> adapter's customers, plan and one-time checkout, customer portal, catalog
> sync, webhook verification, the subscription lifecycle, Connect sales and
> their refund and account events exist; see the
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

With a key, the Stripe adapter implements the same interface:

```go
import payforgestripe "github.com/plexusone/payforge/stripe"

billing, err := payforgestripe.New(spec, payforgestripe.Config{SecretKey: key})
if err != nil { return err }

sess, err := billing.CheckoutPlan(ctx, payforge.PlanCheckout{
	Subject:    payforge.Subject{ID: org.ID, Name: org.Name, Email: org.BillingEmail},
	Plan:       "pro",
	Interval:   pricingspec.IntervalMonth,
	SuccessURL: "https://app.example.com/billing",
	CancelURL:  "https://app.example.com/pricing",
})
// Send the buyer to sess.URL.
```

Prices are found by lookup key, so the catalog must be in the account. Sync it
first, with a dry run to see what would change:

```go
report, err := billing.Sync(ctx, true) // true: report only, write nothing
for _, a := range report.Actions {
	fmt.Println(a.Kind, a.Key, a.Detail)
}
_, err = billing.Sync(ctx, false)
```

Sync is safe to repeat. It never deletes, and it never edits an amount: Stripe
prices are immutable, so a changed amount creates a new price, moves the lookup
key to it and leaves the old one for the subscribers already on it. A subject
that already has a granting subscription is refused with
`payforge.ErrAlreadySubscribed`, and a plan with a trial starts one without
asking for a card.

Webhooks are verified and reduced to a small event:

```go
hook := payforgestripe.Webhook{Secrets: []string{platformSecret, connectSecret}}

func handle(w http.ResponseWriter, r *http.Request) {
	ev, err := hook.Read(r) // verifies the signature; the body is limited to 1 MiB
	if errors.Is(err, payforge.ErrSignatureInvalid) {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	// ev.Kind is a subscription, checkout, invoice, refund or account change,
	// or payforge.EventIgnored for anything else: acknowledge it and move on.
}
```

A subscription event then becomes the account's entitlement, decided from what
Stripe holds now rather than the order webhooks arrived in:

```go
life := &payforge.Lifecycle{
	Spec: spec, Store: store, Sink: sink, // yours: see payforgetest to check them
	Provider: billing,                     // the Stripe client reads subscriptions
}
res, err := life.Handle(ctx, ev)
// err: answer 5xx and Stripe redelivers. Otherwise answer 2xx; res.Outcome is
// applied, duplicate, ignored or skipped, with a Reason.
```

A subscription grants while it is `active`, `trialing` or `past_due`. A price the
spec does not have never grants.
Everything written to the provider carries `payforge_app`, so several products
can share one account and still be told apart.

## Development

```bash
go test ./...
go test . -update   # rewrite the golden files, then review the diff
```
