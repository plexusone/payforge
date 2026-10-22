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
// payforge.ErrAlreadySubscribed, so two browser tabs cannot bill it twice. A
// plan with a trial starts one without asking for a card, and the subscription
// is canceled at the end if the buyer added none.
func (c *Client) CheckoutPlan(ctx context.Context, in payforge.PlanCheckout) (payforge.Session, error) {
	if err := checkSubject(in.Subject); err != nil {
		return payforge.Session{}, err
	}
	if err := checkURL(in.SuccessURL); err != nil {
		return payforge.Session{}, err
	}
	if err := checkURL(in.CancelURL); err != nil {
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
	params.SetIdempotencyKey(idemKey("plan", c.spec.App, in.Subject.ID, plan.ID, string(in.Interval), in.SuccessURL, in.CancelURL))
	sess, err := c.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return payforge.Session{}, fmt.Errorf("stripe: create plan checkout: %w", err)
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
	if err := checkURL(in.SuccessURL); err != nil {
		return payforge.Session{}, err
	}
	if err := checkURL(in.CancelURL); err != nil {
		return payforge.Session{}, err
	}
	var product *pricingspec.Product
	for i := range c.spec.Products {
		if c.spec.Products[i].ID == in.Product {
			product = &c.spec.Products[i]
		}
	}
	if product == nil {
		return payforge.Session{}, fmt.Errorf("%w: %q", payforge.ErrUnknownProduct, in.Product)
	}
	item := &stripesdk.CheckoutSessionCreateLineItemParams{Quantity: stripesdk.Int64(1)}
	if product.SellerSet {
		if in.AmountCents < product.MinAmountCents {
			return payforge.Session{}, fmt.Errorf("%w: %d under %d", payforge.ErrBelowMinimum, in.AmountCents, product.MinAmountCents)
		}
		item.PriceData = &stripesdk.CheckoutSessionCreateLineItemPriceDataParams{
			Currency:   stripesdk.String(c.spec.Currency),
			UnitAmount: stripesdk.Int64(in.AmountCents),
			ProductData: &stripesdk.CheckoutSessionCreateLineItemPriceDataProductDataParams{
				Name:     stripesdk.String(product.Name),
				Metadata: map[string]string{payforge.MetaApp: c.spec.App, payforge.MetaProduct: product.ID},
			},
		}
	} else {
		if in.AmountCents != 0 {
			return payforge.Session{}, payforge.ErrAmountNotAllowed
		}
		price, err := c.priceID(ctx, payforge.ProductLookupKey(c.spec.App, product.ID))
		if err != nil {
			return payforge.Session{}, err
		}
		item.Price = stripesdk.String(price)
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
			fmt.Sprint(in.AmountCents), in.SuccessURL, in.CancelURL))
	}
	sess, err := c.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return payforge.Session{}, fmt.Errorf("stripe: create product checkout: %w", err)
	}
	return payforge.Session{ID: sess.ID, URL: sess.URL}, nil
}
