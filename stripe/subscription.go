package stripe

import (
	"context"
	"fmt"
	"time"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
)

var _ payforge.SubscriptionReader = (*Client)(nil)

// Subscriptions returns a customer's subscriptions that have not ended. It is
// what the lifecycle reads to decide an account's plan from Stripe's state, not
// from the order webhooks arrived in.
func (c *Client) Subscriptions(ctx context.Context, customerID string) ([]payforge.SubscriptionState, error) {
	if customerID == "" {
		return nil, fmt.Errorf("%w: no customer", payforge.ErrInvalidSubject)
	}
	var out []payforge.SubscriptionState
	for sub, err := range c.sc.V1Subscriptions.List(ctx, &stripesdk.SubscriptionListParams{
		Customer: stripesdk.String(customerID),
	}).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("stripe: list subscriptions: %w", err)
		}
		out = append(out, state(sub))
	}
	return out, nil
}

func state(sub *stripesdk.Subscription) payforge.SubscriptionState {
	s := payforge.SubscriptionState{
		ID: sub.ID, Status: string(sub.Status), CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
		TrialEnd: unix(sub.TrialEnd),
	}
	if len(sub.Metadata) > 0 {
		s.Metadata = make(map[string]string, len(sub.Metadata))
		for k, v := range sub.Metadata {
			s.Metadata[k] = v
		}
	}
	if sub.Items == nil {
		return s
	}
	var end time.Time
	for _, it := range sub.Items.Data {
		item := payforge.SubscriptionItem{}
		if it.Price != nil {
			item.PriceID, item.LookupKey = it.Price.ID, it.Price.LookupKey
		}
		s.Items = append(s.Items, item)
		if t := unix(it.CurrentPeriodEnd); t.After(end) {
			end = t
		}
	}
	s.PeriodEnd = end
	return s
}
