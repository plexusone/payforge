package stripe

import (
	"context"
	"fmt"
	"regexp"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
)

var _ payforge.Connect = (*Client)(nil)

// accountPattern is the shape of a connected account ID. It is checked before
// the ID is sent, so a stray value never becomes a request to the wrong path.
var accountPattern = regexp.MustCompile(`^acct_[A-Za-z0-9]{6,64}$`)

func checkAccount(id string) error {
	if !accountPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", payforge.ErrInvalidAccount, id)
	}
	return nil
}

// CreateAccount returns the Express account for a seller, creating it once.
//
// An idempotency key from the app and the seller makes a second call return the
// account the first created. Stripe has no search for accounts, so a host that
// loses the ID cannot look it up again and should store it. The country is the
// seller's two-letter code and cannot be changed later.
func (c *Client) CreateAccount(ctx context.Context, seller payforge.Subject, country string) (string, error) {
	if err := checkSubject(seller); err != nil {
		return "", err
	}
	if !countryPattern.MatchString(country) {
		return "", fmt.Errorf("%w: country %q", payforge.ErrInvalidAccount, country)
	}
	if c.spec.Marketplace == nil {
		return "", payforge.ErrNoMarketplace
	}
	params := &stripesdk.AccountCreateParams{
		Type:    stripesdk.String("express"),
		Country: stripesdk.String(country),
		Capabilities: &stripesdk.AccountCreateCapabilitiesParams{
			CardPayments: &stripesdk.AccountCreateCapabilitiesCardPaymentsParams{Requested: stripesdk.Bool(true)},
			Transfers:    &stripesdk.AccountCreateCapabilitiesTransfersParams{Requested: stripesdk.Bool(true)},
		},
		Metadata: map[string]string{payforge.MetaApp: c.spec.App, payforge.MetaSubject: seller.ID},
	}
	if seller.Email != "" {
		params.Email = stripesdk.String(seller.Email)
	}
	params.SetIdempotencyKey("connect-account-" + c.spec.App + "-" + seller.ID)
	acct, err := c.sc.V1Accounts.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("stripe: create connected account: %w", err)
	}
	return acct.ID, nil
}

var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

// OnboardingLink returns a one-use page where the seller gives Stripe the
// details it needs. The links expire in minutes; refreshURL is where Stripe
// sends the seller when one has, and it should call this again.
func (c *Client) OnboardingLink(ctx context.Context, accountID, refreshURL, returnURL string) (string, error) {
	if err := checkAccount(accountID); err != nil {
		return "", err
	}
	if err := checkURL(refreshURL); err != nil {
		return "", err
	}
	if err := checkURL(returnURL); err != nil {
		return "", err
	}
	link, err := c.sc.V1AccountLinks.Create(ctx, &stripesdk.AccountLinkCreateParams{
		Account:    stripesdk.String(accountID),
		RefreshURL: stripesdk.String(refreshURL),
		ReturnURL:  stripesdk.String(returnURL),
		Type:       stripesdk.String("account_onboarding"),
	})
	if err != nil {
		return "", fmt.Errorf("stripe: create onboarding link: %w", err)
	}
	return link.URL, nil
}

// Account reads a connected account's state.
func (c *Client) Account(ctx context.Context, accountID string) (payforge.AccountState, error) {
	if err := checkAccount(accountID); err != nil {
		return payforge.AccountState{}, err
	}
	acct, err := c.sc.V1Accounts.GetByID(ctx, accountID, nil)
	if err != nil {
		return payforge.AccountState{}, fmt.Errorf("stripe: read connected account: %w", err)
	}
	return payforge.AccountState{
		ID: acct.ID, ChargesEnabled: acct.ChargesEnabled,
		PayoutsEnabled: acct.PayoutsEnabled, DetailsSubmitted: acct.DetailsSubmitted,
	}, nil
}

// CheckoutSale starts a checkout whose proceeds go to the seller.
//
// It is a destination charge made on the seller's behalf: the seller is the
// merchant on the buyer's statement, the money is transferred to the seller's
// account, and the platform's fee is taken as the application fee. The fee is
// the seller plan's rate when it sets one, else the marketplace's, rounded
// down to a cent. A seller whose account cannot take charges is refused with
// payforge.ErrSellerNotReady before any checkout is made.
func (c *Client) CheckoutSale(ctx context.Context, in payforge.Sale) (payforge.Session, error) {
	if err := checkSubject(in.Buyer); err != nil {
		return payforge.Session{}, err
	}
	if err := checkAccount(in.SellerAccount); err != nil {
		return payforge.Session{}, err
	}
	if err := checkURL(in.SuccessURL); err != nil {
		return payforge.Session{}, err
	}
	if err := checkURL(in.CancelURL); err != nil {
		return payforge.Session{}, err
	}
	bps, err := payforge.FeeBps(c.spec, in.SellerPlan)
	if err != nil {
		return payforge.Session{}, err
	}
	product, item, err := c.productLine(ctx, in.Product, in.AmountCents)
	if err != nil {
		return payforge.Session{}, err
	}
	// The fee is on what the buyer pays, which for a fixed-price product is the
	// spec's amount.
	amount := in.AmountCents
	if !product.SellerSet {
		amount = product.AmountCents
	}
	fee, err := payforge.Fee(amount, bps)
	if err != nil {
		return payforge.Session{}, err
	}
	state, err := c.Account(ctx, in.SellerAccount)
	if err != nil {
		return payforge.Session{}, err
	}
	if !state.ChargesEnabled {
		return payforge.Session{}, payforge.ErrSellerNotReady
	}
	customer, err := c.EnsureCustomer(ctx, in.Buyer)
	if err != nil {
		return payforge.Session{}, err
	}
	meta := map[string]string{
		payforge.MetaApp:     c.spec.App,
		payforge.MetaProduct: product.ID,
		payforge.MetaSubject: in.Buyer.ID,
		payforge.MetaSeller:  in.SellerAccount,
	}
	pi := &stripesdk.CheckoutSessionCreatePaymentIntentDataParams{
		Metadata:   meta,
		OnBehalfOf: stripesdk.String(in.SellerAccount),
		TransferData: &stripesdk.CheckoutSessionCreatePaymentIntentDataTransferDataParams{
			Destination: stripesdk.String(in.SellerAccount),
		},
	}
	if fee > 0 {
		pi.ApplicationFeeAmount = stripesdk.Int64(fee)
	}
	params := &stripesdk.CheckoutSessionCreateParams{
		Mode:              stripesdk.String(string(stripesdk.CheckoutSessionModePayment)),
		Customer:          stripesdk.String(customer),
		ClientReferenceID: stripesdk.String(in.Buyer.ID),
		SuccessURL:        stripesdk.String(withSessionID(in.SuccessURL)),
		CancelURL:         stripesdk.String(in.CancelURL),
		LineItems:         []*stripesdk.CheckoutSessionCreateLineItemParams{item},
		Metadata:          meta,
		PaymentIntentData: pi,
	}
	if in.Reference != "" {
		params.SetIdempotencyKey(idemKey("sale", c.spec.App, in.Buyer.ID, in.SellerAccount, product.ID, in.Reference,
			fmt.Sprint(in.AmountCents), fmt.Sprint(fee), in.SuccessURL, in.CancelURL))
	}
	sess, err := c.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return payforge.Session{}, fmt.Errorf("stripe: create sale checkout: %w", err)
	}
	return payforge.Session{ID: sess.ID, URL: sess.URL}, nil
}
