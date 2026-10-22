package payforge

import (
	"context"
	"errors"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// ErrNotConfigured is returned by an operation that needs a payment provider
// when none is configured.
var ErrNotConfigured = errors.New("payforge: billing is not configured")

// Errors a host maps to its own responses.
var (
	// ErrInvalidSubject means the subject has no usable ID.
	ErrInvalidSubject = errors.New("payforge: invalid subject")
	// ErrInvalidURL means a return URL is not an absolute http or https URL.
	ErrInvalidURL = errors.New("payforge: invalid return URL")
	// ErrUnknownPlan means the spec has no such plan.
	ErrUnknownPlan = errors.New("payforge: unknown plan")
	// ErrNotSelfServe means the plan cannot be bought through checkout.
	ErrNotSelfServe = errors.New("payforge: plan is not self-serve")
	// ErrNoPrice means the plan has no price for the interval.
	ErrNoPrice = errors.New("payforge: plan has no price for the interval")
	// ErrUnknownProduct means the spec has no such one-time product.
	ErrUnknownProduct = errors.New("payforge: unknown product")
	// ErrBelowMinimum means a seller-set amount is under the product's minimum.
	ErrBelowMinimum = errors.New("payforge: amount is below the product's minimum")
	// ErrAmountNotAllowed means an amount was given for a product whose price is fixed.
	ErrAmountNotAllowed = errors.New("payforge: product has a fixed price")
	// ErrNotSynced means the provider has no price for a lookup key: the
	// catalog has not been synced to the account.
	ErrNotSynced = errors.New("payforge: price not found at the provider; sync the catalog")
	// ErrAlreadySubscribed means the subject already holds a granting
	// subscription, so a second checkout would bill it twice.
	ErrAlreadySubscribed = errors.New("payforge: subject already has a subscription")
)

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

// ProductCheckout asks for a one-time purchase of a product from the spec.
type ProductCheckout struct {
	Subject Subject
	Product string
	// AmountCents is the buyer's or seller's chosen amount, for a seller-set
	// product only; it must reach the product's minimum. Leave it zero for a
	// product with a fixed price.
	AmountCents int64
	// Reference is the host's own identifier for this purchase. Retrying a
	// checkout with the same reference returns the same session, so a double
	// click does not create two.
	Reference  string
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
	// CheckoutProduct starts a one-time purchase checkout.
	CheckoutProduct(ctx context.Context, c ProductCheckout) (Session, error)
	// EnsureCustomer returns the provider's customer for a subject, creating it
	// once. The host may keep the ID, but need not.
	EnsureCustomer(ctx context.Context, s Subject) (string, error)
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

func (disabled) CheckoutProduct(context.Context, ProductCheckout) (Session, error) {
	return Session{}, ErrNotConfigured
}

func (disabled) EnsureCustomer(context.Context, Subject) (string, error) {
	return "", ErrNotConfigured
}
