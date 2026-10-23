package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// idemKey derives a stable idempotency key from the parts that define a
// request, so the same request returns the same object and a different one
// (another URL, say) gets its own instead of an idempotency conflict.
func idemKey(kind string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return kind + "-" + hex.EncodeToString(sum[:16])
}

// CheckoutPlan starts a subscription checkout for a self-serve plan.
//
// The price is found by lookup key, so the catalog must have been synced to the
// account. A subject that already holds a granting subscription is refused with
// payforge.ErrAlreadySubscribed. That check is made before the session exists, so
// it stops a second checkout after the first has been paid, not two that are open
// at once: a host that must never bill twice holds a lock per subject around
// checkout. A plan with a trial starts one without asking for a card, and the
// subscription is canceled at the end if the buyer added none.
func (c *Client) CheckoutPlan(ctx context.Context, in payforge.PlanCheckout) (payforge.Session, error) {
	if err := checkSubject(in.Subject); err != nil {
		return payforge.Session{}, err
	}
	if err := c.checkURL(in.SuccessURL); err != nil {
		return payforge.Session{}, err
	}
	if err := c.checkURL(in.CancelURL); err != nil {
		return payforge.Session{}, err
	}
	plan, ok := c.spec.Plan(in.Plan)
	if !ok {
		return payforge.Session{}, fmt.Errorf("%w: %q", payforge.ErrUnknownPlan, in.Plan)
	}
	if !plan.SelfServe {
		return payforge.Session{}, fmt.Errorf("%w: %q", payforge.ErrNotSelfServe, in.Plan)
	}
	if !hasPrice(plan, in.Interval) {
		return payforge.Session{}, fmt.Errorf("%w: %s %s", payforge.ErrNoPrice, in.Plan, in.Interval)
	}
	price, err := c.priceID(ctx, payforge.PlanLookupKey(c.spec.App, plan.ID, in.Interval))
	if err != nil {
		return payforge.Session{}, err
	}
	customer, err := c.EnsureCustomer(ctx, in.Subject)
	if err != nil {
		return payforge.Session{}, err
	}
	if err := c.refuseIfSubscribed(ctx, customer); err != nil {
		return payforge.Session{}, err
	}

	meta := map[string]string{
		payforge.MetaApp:     c.spec.App,
		payforge.MetaPlan:    plan.ID,
		payforge.MetaSubject: in.Subject.ID,
	}
	sub := &stripesdk.CheckoutSessionCreateSubscriptionDataParams{Metadata: meta}
	params := &stripesdk.CheckoutSessionCreateParams{
		Mode:              stripesdk.String(string(stripesdk.CheckoutSessionModeSubscription)),
		Customer:          stripesdk.String(customer),
		ClientReferenceID: stripesdk.String(in.Subject.ID),
		SuccessURL:        stripesdk.String(withSessionID(in.SuccessURL)),
		CancelURL:         stripesdk.String(in.CancelURL),
		LineItems: []*stripesdk.CheckoutSessionCreateLineItemParams{{
			Price: stripesdk.String(price), Quantity: stripesdk.Int64(1),
		}},
		Metadata: meta,
		// The subscription carries the same metadata, so its own events can
		// be matched without the checkout session.
		SubscriptionData: sub,
	}
	if plan.TrialDays > 0 {
		sub.TrialPeriodDays = stripesdk.Int64(int64(plan.TrialDays))
		sub.TrialSettings = &stripesdk.CheckoutSessionCreateSubscriptionDataTrialSettingsParams{
			EndBehavior: &stripesdk.CheckoutSessionCreateSubscriptionDataTrialSettingsEndBehaviorParams{
				MissingPaymentMethod: stripesdk.String("cancel"),
			},
		}
		params.PaymentMethodCollection = stripesdk.String("if_required")
	}
	key := idemKey("plan", c.spec.App, in.Subject.ID, plan.ID, string(in.Interval), price, in.SuccessURL, in.CancelURL)
	sess, err := c.createSession(ctx, params, key)
	if err != nil {
		return payforge.Session{}, fmt.Errorf("stripe: create plan checkout: %w", err)
	}
	return sess, nil
}

// createSession creates a checkout session under an idempotency key. Stripe
// replays a key's response for a day, and a session that was completed or has
// expired has no URL, so a buyer who subscribes, cancels and comes back the same
// day would be handed a dead session. In that case it creates a new one under a
// key derived from the dead one.
func (c *Client) createSession(ctx context.Context, params *stripesdk.CheckoutSessionCreateParams, key string) (payforge.Session, error) {
	params.SetIdempotencyKey(key)
	sess, err := c.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return payforge.Session{}, err
	}
	if sess.URL == "" && sess.ID != "" {
		params.SetIdempotencyKey(idemKey("retry", key, sess.ID))
		if sess, err = c.sc.V1CheckoutSessions.Create(ctx, params); err != nil {
			return payforge.Session{}, err
		}
	}
	return payforge.Session{ID: sess.ID, URL: sess.URL}, nil
}

func hasPrice(p pricingspec.Plan, iv pricingspec.Interval) bool {
	for _, pr := range p.Prices {
		if pr.Interval == iv {
			return true
		}
	}
	return false
}

// refuseIfSubscribed returns ErrAlreadySubscribed when the customer has a
// subscription that grants its plan.
func (c *Client) refuseIfSubscribed(ctx context.Context, customer string) error {
	for sub, err := range c.sc.V1Subscriptions.List(ctx, &stripesdk.SubscriptionListParams{
		Customer: stripesdk.String(customer),
	}).All(ctx) {
		if err != nil {
			return fmt.Errorf("stripe: list subscriptions: %w", err)
		}
		if payforge.Grants(string(sub.Status)) {
			return payforge.ErrAlreadySubscribed
		}
	}
	return nil
}

// CheckoutProduct starts a one-time purchase checkout.
//
// A fixed-price product is sold at its synced price. A seller-set product is
// sold at the amount given, which must reach the product's minimum. A
// Reference makes a retry return the same session.
func (c *Client) CheckoutProduct(ctx context.Context, in payforge.ProductCheckout) (payforge.Session, error) {
	if err := checkSubject(in.Subject); err != nil {
		return payforge.Session{}, err
	}
	if err := c.checkURL(in.SuccessURL); err != nil {
		return payforge.Session{}, err
	}
	if err := c.checkURL(in.CancelURL); err != nil {
		return payforge.Session{}, err
	}
	product, item, err := c.productLine(ctx, in.Product, in.AmountCents)
	if err != nil {
		return payforge.Session{}, err
	}
	customer, err := c.EnsureCustomer(ctx, in.Subject)
	if err != nil {
		return payforge.Session{}, err
	}
	meta := map[string]string{
		payforge.MetaApp:     c.spec.App,
		payforge.MetaProduct: product.ID,
		payforge.MetaSubject: in.Subject.ID,
	}
	params := &stripesdk.CheckoutSessionCreateParams{
		Mode:              stripesdk.String(string(stripesdk.CheckoutSessionModePayment)),
		Customer:          stripesdk.String(customer),
		ClientReferenceID: stripesdk.String(in.Subject.ID),
		SuccessURL:        stripesdk.String(withSessionID(in.SuccessURL)),
		CancelURL:         stripesdk.String(in.CancelURL),
		LineItems:         []*stripesdk.CheckoutSessionCreateLineItemParams{item},
		Metadata:          meta,
		PaymentIntentData: &stripesdk.CheckoutSessionCreatePaymentIntentDataParams{Metadata: meta},
	}
	if in.Reference != "" {
		params.SetIdempotencyKey(idemKey("product", c.spec.App, in.Subject.ID, product.ID, in.Reference,
			fmt.Sprint(in.AmountCents), linePrice(item), in.SuccessURL, in.CancelURL))
	}
	sess, err := c.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return payforge.Session{}, fmt.Errorf("stripe: create product checkout: %w", err)
	}
	return payforge.Session{ID: sess.ID, URL: sess.URL}, nil
}

// productLine finds a product in the spec and builds its checkout line: a
// fixed-price product at its synced price, a seller-set product at the amount
// given, which must reach the product's minimum.
func (c *Client) productLine(ctx context.Context, id string, amount int64) (*pricingspec.Product, *stripesdk.CheckoutSessionCreateLineItemParams, error) {
	var product *pricingspec.Product
	for i := range c.spec.Products {
		if c.spec.Products[i].ID == id {
			product = &c.spec.Products[i]
		}
	}
	if product == nil {
		return nil, nil, fmt.Errorf("%w: %q", payforge.ErrUnknownProduct, id)
	}
	item := &stripesdk.CheckoutSessionCreateLineItemParams{Quantity: stripesdk.Int64(1)}
	if product.SellerSet {
		if amount < product.MinAmountCents {
			return nil, nil, fmt.Errorf("%w: %d under %d", payforge.ErrBelowMinimum, amount, product.MinAmountCents)
		}
		item.PriceData = &stripesdk.CheckoutSessionCreateLineItemPriceDataParams{
			Currency:   stripesdk.String(c.spec.Currency),
			UnitAmount: stripesdk.Int64(amount),
			ProductData: &stripesdk.CheckoutSessionCreateLineItemPriceDataProductDataParams{
				Name:     stripesdk.String(product.Name),
				Metadata: map[string]string{payforge.MetaApp: c.spec.App, payforge.MetaProduct: product.ID},
			},
		}
		return product, item, nil
	}
	if amount != 0 {
		return nil, nil, payforge.ErrAmountNotAllowed
	}
	price, err := c.priceID(ctx, payforge.ProductLookupKey(c.spec.App, product.ID))
	if err != nil {
		return nil, nil, err
	}
	item.Price = stripesdk.String(price)
	return product, item, nil
}

// linePrice is the synced price a line uses, empty for a seller-set amount. A key
// that includes it is not replayed against a different price after a sync moves
// the lookup key.
func linePrice(item *stripesdk.CheckoutSessionCreateLineItemParams) string {
	if item.Price != nil {
		return *item.Price
	}
	return ""
}
