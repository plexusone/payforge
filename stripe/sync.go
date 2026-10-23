package stripe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
)

// ErrForeign means an object at the account holds a name payforge uses but
// belongs to another product, so sync leaves it alone and stops.
var ErrForeign = errors.New("stripe: object belongs to another app")

// ErrArchived means a product the spec needs is archived at the account. Sync
// does not bring archived objects back; unarchive it or choose another plan ID.
var ErrArchived = errors.New("stripe: product is archived")

// ErrDeleted means a product was deleted at Stripe. A deleted product's ID cannot
// be used again, so the plan needs a new ID in the spec.
var ErrDeleted = errors.New("stripe: product was deleted")

// SyncKind is what sync did, or would do, to one object.
type SyncKind string

const (
	SyncCreateProduct SyncKind = "create_product"
	SyncUpdateProduct SyncKind = "update_product"
	SyncCreatePrice   SyncKind = "create_price"
	// SyncReplacePrice creates a new price because a price is immutable and the
	// spec's amount, currency or interval changed, and moves the lookup key to
	// it. The old price stays for the subscribers already on it.
	SyncReplacePrice SyncKind = "replace_price"
	SyncUnchanged    SyncKind = "unchanged"
)

// SyncAction is one step of a sync.
type SyncAction struct {
	Kind SyncKind
	// Key is the product key or the price's lookup key.
	Key    string
	Detail string
}

// SyncReport lists what a sync did, in a stable order.
type SyncReport struct {
	DryRun  bool
	Actions []SyncAction
}

// Changed reports whether the sync wrote, or in a dry run would write, anything.
func (r SyncReport) Changed() bool {
	for _, a := range r.Actions {
		if a.Kind != SyncUnchanged {
			return true
		}
	}
	return false
}

// ProductID is the ID of the Stripe product for a product key from
// payforge.ProviderPlanFor. Stripe lets the creator choose a product's ID, and
// a product cannot be looked up by key, so the ID is derived from the key.
func ProductID(key string) string {
	return "payforge_" + strings.ReplaceAll(key, "/", "_")
}

// Sync makes the Stripe account hold what the spec describes: a product per
// priced plan and per fixed one-time product, and a price per interval found by
// its lookup key.
//
// Sync is idempotent. It never deletes and never edits an amount: a price is
// immutable at Stripe, so a changed amount creates a new price, moves the lookup
// key to it and leaves the old price for existing subscribers. With dryRun it
// reads the account and reports what it would do without writing.
//
// If a step fails, the report holds the steps taken so far along with the error.
func (c *Client) Sync(ctx context.Context, dryRun bool) (SyncReport, error) {
	report := SyncReport{DryRun: dryRun}
	want := payforge.ProviderPlanFor(c.spec)
	for _, p := range want.Products {
		if err := c.syncProduct(ctx, p, dryRun, &report); err != nil {
			return report, err
		}
	}
	for _, p := range want.Prices {
		if err := c.syncPrice(ctx, p, dryRun, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

func isMissing(err error) bool {
	var se *stripesdk.Error
	return errors.As(err, &se) && se.Code == stripesdk.ErrorCodeResourceMissing
}

func (c *Client) syncProduct(ctx context.Context, p payforge.ProviderProduct, dryRun bool, r *SyncReport) error {
	id := ProductID(p.Key)
	have, err := c.sc.V1Products.Retrieve(ctx, id, nil)
	if err != nil && !isMissing(err) {
		return fmt.Errorf("stripe: read product %s: %w", p.Key, err)
	}
	if err != nil { // missing
		r.Actions = append(r.Actions, SyncAction{Kind: SyncCreateProduct, Key: p.Key, Detail: p.Name})
		if dryRun {
			return nil
		}
		params := &stripesdk.ProductCreateParams{ID: stripesdk.String(id), Name: stripesdk.String(p.Name), Metadata: p.Metadata}
		params.SetIdempotencyKey("product-" + id)
		if _, err := c.sc.V1Products.Create(ctx, params); err != nil {
			return fmt.Errorf("stripe: create product %s: %w", p.Key, err)
		}
		return nil
	}
	if app := have.Metadata[payforge.MetaApp]; app != "" && app != c.spec.App {
		return fmt.Errorf("%w: product %s is for %q", ErrForeign, id, app)
	}
	if have.Deleted {
		return fmt.Errorf("%w: %s", ErrDeleted, id)
	}
	if !have.Active {
		return fmt.Errorf("%w: %s", ErrArchived, id)
	}
	var detail []string
	upd := &stripesdk.ProductUpdateParams{}
	if have.Name != p.Name {
		upd.Name = stripesdk.String(p.Name)
		detail = append(detail, fmt.Sprintf("name %q to %q", have.Name, p.Name))
	}
	for k, v := range p.Metadata {
		if have.Metadata[k] != v {
			upd.AddMetadata(k, v)
			detail = append(detail, "metadata "+k)
		}
	}
	if len(detail) == 0 {
		r.Actions = append(r.Actions, SyncAction{Kind: SyncUnchanged, Key: p.Key})
		return nil
	}
	r.Actions = append(r.Actions, SyncAction{Kind: SyncUpdateProduct, Key: p.Key, Detail: strings.Join(detail, ", ")})
	if dryRun {
		return nil
	}
	if _, err := c.sc.V1Products.Update(ctx, id, upd); err != nil {
		return fmt.Errorf("stripe: update product %s: %w", p.Key, err)
	}
	return nil
}

func (c *Client) syncPrice(ctx context.Context, p payforge.ProviderPrice, dryRun bool, r *SyncReport) error {
	var have *stripesdk.Price
	for pr, err := range c.sc.V1Prices.List(ctx, &stripesdk.PriceListParams{
		Active:     stripesdk.Bool(true),
		LookupKeys: []*string{stripesdk.String(p.LookupKey)},
		ListParams: stripesdk.ListParams{Limit: stripesdk.Int64(1)},
	}).All(ctx) {
		if err != nil {
			return fmt.Errorf("stripe: read price %s: %w", p.LookupKey, err)
		}
		have = pr
		break
	}
	productID := ProductID(p.ProductKey)
	kind, detail := SyncCreatePrice, describe(p)
	if have != nil {
		if app := have.Metadata[payforge.MetaApp]; app != "" && app != c.spec.App {
			return fmt.Errorf("%w: price %s is for %q", ErrForeign, p.LookupKey, app)
		}
		if same(have, p, productID) {
			r.Actions = append(r.Actions, SyncAction{Kind: SyncUnchanged, Key: p.LookupKey})
			return nil
		}
		kind, detail = SyncReplacePrice, describePrice(have)+" to "+describe(p)
	}
	r.Actions = append(r.Actions, SyncAction{Kind: kind, Key: p.LookupKey, Detail: detail})
	if dryRun {
		return nil
	}
	params := &stripesdk.PriceCreateParams{
		Currency:          stripesdk.String(p.Currency),
		UnitAmount:        stripesdk.Int64(p.AmountCents),
		Product:           stripesdk.String(productID),
		LookupKey:         stripesdk.String(p.LookupKey),
		TransferLookupKey: stripesdk.Bool(true),
		Metadata:          p.Metadata,
	}
	if p.Interval != "" {
		params.Recurring = &stripesdk.PriceCreateRecurringParams{Interval: stripesdk.String(string(p.Interval))}
	}
	// The price that holds the key now is part of the idempotency key: without it,
	// a price changed from A to B and back to A inside a day would replay the
	// first creation of A, which moves nothing, and the key would stay on B.
	held := ""
	if have != nil {
		held = have.ID
	}
	params.SetIdempotencyKey(idemKey("price", p.LookupKey, p.Currency, fmt.Sprint(p.AmountCents), string(p.Interval), productID, held))
	if _, err := c.sc.V1Prices.Create(ctx, params); err != nil {
		return fmt.Errorf("stripe: create price %s: %w", p.LookupKey, err)
	}
	return nil
}

// same reports whether an existing price already is what the spec wants. A price
// is immutable, so any difference in these fields needs a new one.
func same(have *stripesdk.Price, want payforge.ProviderPrice, productID string) bool {
	if have.UnitAmount != want.AmountCents || string(have.Currency) != want.Currency {
		return false
	}
	if have.Product == nil || have.Product.ID != productID {
		return false
	}
	if want.Interval == "" {
		return have.Recurring == nil
	}
	return have.Recurring != nil && string(have.Recurring.Interval) == string(want.Interval) &&
		(have.Recurring.IntervalCount == 0 || have.Recurring.IntervalCount == 1)
}

func describe(p payforge.ProviderPrice) string {
	s := fmt.Sprintf("%d %s", p.AmountCents, p.Currency)
	if p.Interval != "" {
		s += "/" + string(p.Interval)
	}
	return s
}

func describePrice(p *stripesdk.Price) string {
	s := fmt.Sprintf("%d %s", p.UnitAmount, p.Currency)
	if p.Recurring != nil {
		s += "/" + string(p.Recurring.Interval)
	}
	return s
}
