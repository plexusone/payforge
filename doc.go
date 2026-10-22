// Package payforge is the billing runtime for a product whose pricing and
// packaging is described by a pricing spec
// (github.com/plexusone/systemspec-pricingpackaging).
//
// It owns what happens at the payment provider and in the subscription
// lifecycle: creating the provider's products and prices from the spec,
// checkout and the customer portal, and turning a paid subscription into an
// entitlement through ports the host supplies. The provider-neutral core is
// this package; the Stripe adapter is in payforge/stripe.
//
// A host that has no provider configured runs in free mode: every account is on
// the spec's default plan.
package payforge
