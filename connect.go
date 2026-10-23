package payforge

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

var (
	// ErrNoMarketplace means the spec has no marketplace section, so there is no
	// platform fee to take and no sale to make.
	ErrNoMarketplace = errors.New("payforge: the spec has no marketplace")
	// ErrInvalidAccount means a connected account ID is empty or malformed.
	ErrInvalidAccount = errors.New("payforge: invalid connected account")
	// ErrForeignAccount means a connected account was not created by this app, so
	// it is not paid.
	ErrForeignAccount = errors.New("payforge: the connected account does not belong to this app")
	// ErrSellerNotReady means the seller's account cannot take charges yet, so
	// it has not finished onboarding or the provider has paused it.
	ErrSellerNotReady = errors.New("payforge: the seller's account cannot take charges")
)

// Sale is a purchase from a seller who has a connected account. The money goes
// to the seller; the platform keeps its fee.
type Sale struct {
	// Buyer is the account that pays.
	Buyer Subject
	// SellerAccount is the seller's connected account ID.
	SellerAccount string
	// SellerPlan is the plan the seller is on, which can set its own fee. An
	// empty or unknown plan uses the marketplace fee. Take it from your own record
	// of the seller: a value from a request would let a seller choose their fee.
	SellerPlan string
	Product    string
	// AmountCents follows ProductCheckout.AmountCents.
	AmountCents int64
	// Reference follows ProductCheckout.Reference.
	Reference  string
	SuccessURL string
	CancelURL  string
}

// Connect is what a host calls to sell for others. The Stripe adapter
// implements it.
type Connect interface {
	// CreateAccount returns the seller's connected account, creating it once.
	CreateAccount(ctx context.Context, seller Subject, country string) (string, error)
	// OnboardingLink returns a one-use page where the seller gives the provider
	// the details it needs. Call it again for a fresh link when one expires.
	OnboardingLink(ctx context.Context, accountID, refreshURL, returnURL string) (string, error)
	// Account reads a connected account's current state.
	Account(ctx context.Context, accountID string) (AccountState, error)
	// CheckoutSale starts a checkout whose proceeds go to the seller.
	CheckoutSale(ctx context.Context, s Sale) (Session, error)
}

// FeeBps returns the platform fee for a sale by a seller on the given plan, in
// basis points: the plan's own fee when it sets one, else the marketplace's. It
// fails with ErrNoMarketplace when the spec has none.
func FeeBps(spec *pricingspec.Spec, sellerPlan string) (int, error) {
	if spec.Marketplace == nil {
		return 0, ErrNoMarketplace
	}
	if p, ok := spec.Plan(sellerPlan); ok && p.PlatformFeeBps != nil {
		return *p.PlatformFeeBps, nil
	}
	return spec.Marketplace.PlatformFeeBps, nil
}

// Fee returns the platform's share of an amount at a rate in basis points,
// rounded down to a whole cent, so the seller never receives less than the
// exact share and the platform never takes more.
func Fee(amountCents int64, bps int) (int64, error) {
	if amountCents < 0 || amountCents > math.MaxInt64/10000 || bps < 0 || bps > 10000 {
		return 0, fmt.Errorf("payforge: fee of %d cents at %d bps is out of range", amountCents, bps)
	}
	return amountCents * int64(bps) / 10000, nil
}
