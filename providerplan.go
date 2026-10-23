package payforge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// Metadata keys written to every object payforge creates at the provider.
const (
	// MetaApp is the spec's product identifier, so several products can share
	// one provider account and still be told apart.
	MetaApp = "payforge_app"
	// MetaPlan is the plan an object was created for.
	MetaPlan = "payforge_plan"
	// MetaProduct is the one-time product an object was created for.
	MetaProduct = "payforge_product"
	// MetaSeller is the connected account a sale's proceeds go to.
	MetaSeller = "payforge_seller"
	// MetaSubject is the host's account identifier, on customers and
	// subscriptions.
	MetaSubject = "payforge_subject"
)

// ProviderProduct is a product the provider should hold.
type ProviderProduct struct {
	// Key identifies the product across syncs: "<app>/<plan>" or
	// "<app>/<product>".
	Key      string
	Name     string
	Metadata map[string]string
}

// ProviderPrice is a price the provider should hold. A provider price is
// immutable, so a changed amount is a new price that takes over the lookup key.
type ProviderPrice struct {
	// LookupKey finds the price: "<app>/<plan>/<interval>" for a plan and
	// "<app>/<product>" for a fixed one-time product.
	LookupKey   string
	ProductKey  string
	Currency    string
	AmountCents int64
	// Interval is empty for a one-time price.
	Interval pricingspec.Interval
	Metadata map[string]string
}

// ProviderPlan is what a provider account should hold for a spec. Seller-set
// products have no price here: their amount is chosen at checkout.
type ProviderPlan struct {
	Products []ProviderProduct
	Prices   []ProviderPrice
}

// PlanLookupKey is the lookup key of a plan's price for an interval.
func PlanLookupKey(app, plan string, interval pricingspec.Interval) string {
	return fmt.Sprintf("%s/%s/%s", app, plan, interval)
}

// ProductLookupKey is the lookup key of a fixed one-time product's price.
func ProductLookupKey(app, product string) string {
	return fmt.Sprintf("%s/%s", app, product)
}

// ParsePlanLookupKey splits a plan price's lookup key. ok is false for any other
// key, so a price someone else created is never mistaken for one of ours.
func ParsePlanLookupKey(key string) (app, plan string, interval pricingspec.Interval, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return "", "", "", false
	}
	iv := pricingspec.Interval(parts[2])
	if iv != pricingspec.IntervalMonth && iv != pricingspec.IntervalYear {
		return "", "", "", false
	}
	return parts[0], parts[1], iv, parts[0] != "" && parts[1] != ""
}

// ProviderPlanFor computes what a provider account should hold for the spec: a
// product and a price per interval for every plan with prices, and a product and
// a price for every fixed one-time product. The order is stable.
func ProviderPlanFor(spec *pricingspec.Spec) ProviderPlan {
	var out ProviderPlan
	for _, p := range spec.Plans {
		if len(p.Prices) == 0 {
			continue
		}
		key := spec.App + "/" + p.ID
		meta := map[string]string{MetaApp: spec.App, MetaPlan: p.ID}
		out.Products = append(out.Products, ProviderProduct{Key: key, Name: p.Name, Metadata: meta})
		prices := make([]pricingspec.Price, len(p.Prices))
		copy(prices, p.Prices)
		sort.SliceStable(prices, func(i, j int) bool { return prices[i].Interval < prices[j].Interval })
		for _, pr := range prices {
			out.Prices = append(out.Prices, ProviderPrice{
				LookupKey: PlanLookupKey(spec.App, p.ID, pr.Interval), ProductKey: key,
				Currency: spec.Currency, AmountCents: pr.AmountCents, Interval: pr.Interval,
				Metadata: map[string]string{MetaApp: spec.App, MetaPlan: p.ID},
			})
		}
	}
	for _, pr := range spec.Products {
		if pr.SellerSet {
			continue
		}
		key := spec.App + "/" + pr.ID
		meta := map[string]string{MetaApp: spec.App, MetaProduct: pr.ID}
		out.Products = append(out.Products, ProviderProduct{Key: key, Name: pr.Name, Metadata: meta})
		out.Prices = append(out.Prices, ProviderPrice{
			LookupKey: ProductLookupKey(spec.App, pr.ID), ProductKey: key,
			Currency: spec.Currency, AmountCents: pr.AmountCents,
			Metadata: map[string]string{MetaApp: spec.App, MetaProduct: pr.ID},
		})
	}
	return out
}
