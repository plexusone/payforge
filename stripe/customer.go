package stripe

import (
	"context"
	"fmt"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
)

// EnsureCustomer returns the Stripe customer for a subject, creating it once.
//
// It searches by the subject's metadata first, then creates with an idempotency
// key. Stripe's search can lag a creation by up to a minute, so the key covers
// that gap: a second call in the meantime gets the customer the first created.
// A host that stores the customer ID need not call this again.
func (c *Client) EnsureCustomer(ctx context.Context, s payforge.Subject) (string, error) {
	if err := checkSubject(s); err != nil {
		return "", err
	}
	// The subject ID matched subjectPattern, and the app is a spec identifier,
	// so neither can contain a quote.
	query := fmt.Sprintf("metadata['%s']:'%s' AND metadata['%s']:'%s'",
		payforge.MetaApp, c.spec.App, payforge.MetaSubject, s.ID)
	for cust, err := range c.sc.V1Customers.Search(ctx, &stripesdk.CustomerSearchParams{
		SearchParams: stripesdk.SearchParams{Query: query, Limit: stripesdk.Int64(1)},
	}).All(ctx) {
		if err != nil {
			return "", fmt.Errorf("stripe: search customers: %w", err)
		}
		return cust.ID, nil
	}
	params := &stripesdk.CustomerCreateParams{
		Metadata: map[string]string{payforge.MetaApp: c.spec.App, payforge.MetaSubject: s.ID},
	}
	if s.Name != "" {
		params.Name = stripesdk.String(s.Name)
	}
	if s.Email != "" {
		params.Email = stripesdk.String(s.Email)
	}
	params.SetIdempotencyKey("customer-" + c.spec.App + "-" + s.ID)
	cust, err := c.sc.V1Customers.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("stripe: create customer: %w", err)
	}
	return cust.ID, nil
}

// Portal opens the customer portal for a customer.
func (c *Client) Portal(ctx context.Context, customerID, returnURL string) (string, error) {
	if customerID == "" {
		return "", fmt.Errorf("%w: no customer", payforge.ErrInvalidSubject)
	}
	if err := checkURL(returnURL); err != nil {
		return "", err
	}
	sess, err := c.sc.V1BillingPortalSessions.Create(ctx, &stripesdk.BillingPortalSessionCreateParams{
		Customer:  stripesdk.String(customerID),
		ReturnURL: stripesdk.String(returnURL),
	})
	if err != nil {
		return "", fmt.Errorf("stripe: create portal session: %w", err)
	}
	return sess.URL, nil
}
