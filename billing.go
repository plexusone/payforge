package payforge

import (
	"context"
	"errors"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// ErrNotConfigured is returned by an operation that needs a payment provider
// when none is configured.
var ErrNotConfigured = errors.New("payforge: billing is not configured")

// Subject is the host's account that pays: an organization or a person.
type Subject struct {
	// ID is the host's identifier. It is kept on the provider's customer so
	// events can be matched to the account.
	ID    string
	Name  string
	Email string
}

// PlanCheckout asks for a subscription to a self-serve plan.
type PlanCheckout struct {
	Subject    Subject
	Plan       string
	Interval   pricingspec.Interval
	SuccessURL string
	CancelURL  string
}

// Session is a hosted page to send the buyer to.
type Session struct {
	ID  string
	URL string
}

// Billing is what a host calls. The Stripe adapter implements it; Disabled
// returns the free-mode implementation.
type Billing interface {
	// Enabled reports whether a payment provider is configured.
	Enabled() bool
	// Plan resolves the plan an account has stored. Without a provider every
	// account is on the spec's default plan; with one, an unknown plan is the
	// default plan too.
	Plan(stored string) pricingspec.Plan
	// CheckoutPlan starts a subscription checkout.
	CheckoutPlan(ctx context.Context, c PlanCheckout) (Session, error)
	// Portal opens the provider's customer portal for a customer.
	Portal(ctx context.Context, customerID, returnURL string) (string, error)
}

// Disabled returns the free-mode Billing for a spec: every account is on the
// spec's default plan, and nothing that needs a provider works. A host can test
// Enabled to hide upgrade prompts.
func Disabled(spec *pricingspec.Spec) Billing { return disabled{spec: spec} }

type disabled struct{ spec *pricingspec.Spec }

func (disabled) Enabled() bool { return false }

func (d disabled) Plan(string) pricingspec.Plan { return d.spec.PlanOrDefault("") }

func (disabled) CheckoutPlan(context.Context, PlanCheckout) (Session, error) {
	return Session{}, ErrNotConfigured
}

func (disabled) Portal(context.Context, string, string) (string, error) {
	return "", ErrNotConfigured
}
