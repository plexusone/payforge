package stripe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/plexusone/payforge/stripe"
)

func kinds(r stripe.SyncReport) map[stripe.SyncKind]int {
	m := map[stripe.SyncKind]int{}
	for _, a := range r.Actions {
		m[a.Kind]++
	}
	return m
}

func TestSyncCreatesTheCatalogOnAnEmptyAccount(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)

	r, err := c.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	// Two priced plans (plus, business) with a month and a year price each.
	if k := kinds(r); k[stripe.SyncCreateProduct] != 2 || k[stripe.SyncCreatePrice] != 4 || len(r.Actions) != 6 {
		t.Fatalf("actions = %+v", r.Actions)
	}
	if !r.Changed() || r.DryRun {
		t.Fatalf("report = %+v", r)
	}

	prod := f.products["payforge_taskboard_plus"]
	if prod == nil || prod["name"] != "Plus" {
		t.Fatalf("product = %v", prod)
	}
	if m := prod["metadata"].(map[string]any); m["payforge_app"] != "taskboard" || m["payforge_plan"] != "plus" {
		t.Fatalf("product metadata = %v", m)
	}
	price := f.priceObjs["taskboard/plus/year"]
	if price == nil || price["unit_amount"] != int64(12000) || price["currency"] != "usd" || price["product"] != "payforge_taskboard_plus" {
		t.Fatalf("price = %v", price)
	}
	if price["recurring"].(map[string]any)["interval"] != "year" {
		t.Fatalf("recurring = %v", price["recurring"])
	}
	create := f.last("POST", "/v1/prices")
	if create.Form["transfer_lookup_key"] != "true" || create.Form["metadata[payforge_app]"] != "taskboard" || create.IdemKey == "" {
		t.Fatalf("price create = %+v", create)
	}

	// The synced prices are what checkout finds.
	if _, err := c.CheckoutPlan(ctx, planIn("plus")); err != nil {
		t.Fatalf("checkout after sync: %v", err)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "marketplace.json"), srv)
	if _, err := c.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	writes := f.count("POST", "/v1/products") + f.count("POST", "/v1/prices")
	r, err := c.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed() {
		t.Fatalf("second sync changed things: %+v", r.Actions)
	}
	if got := f.count("POST", "/v1/products") + f.count("POST", "/v1/prices") + f.count("POST", "/v1/products/payforge_craftmarket_seller"); got != writes {
		t.Fatalf("second sync wrote: %d writes before, %d after", writes, got)
	}
	// A seller-set product has no price to sync; the fixed one does.
	if _, ok := f.priceObjs["craftmarket/sample_pack"]; !ok {
		t.Error("the fixed product has no price")
	}
	if _, ok := f.priceObjs["craftmarket/listing"]; ok {
		t.Error("a seller-set product got a price")
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	r, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun || !r.Changed() || len(r.Actions) != 6 {
		t.Fatalf("report = %+v", r)
	}
	for _, c := range f.calls {
		if c.Method == "POST" {
			t.Fatalf("a dry run wrote: %+v", c)
		}
	}
}

func TestSyncReplacesAChangedPriceAndKeepsTheOldOne(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	if _, err := c.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	// The account holds an older amount for one price.
	f.priceObjs["taskboard/plus/month"]["unit_amount"] = int64(900)
	old := f.priceObjs["taskboard/plus/month"]["id"]

	r, err := c.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(r); k[stripe.SyncReplacePrice] != 1 || k[stripe.SyncUnchanged] != 5 {
		t.Fatalf("actions = %+v", r.Actions)
	}
	for _, a := range r.Actions {
		if a.Kind == stripe.SyncReplacePrice && (a.Key != "taskboard/plus/month" || a.Detail != "900 usd/month to 1200 usd/month") {
			t.Fatalf("action = %+v", a)
		}
	}
	got := f.priceObjs["taskboard/plus/month"]
	if got["id"] == old || got["unit_amount"] != int64(1200) {
		t.Fatalf("lookup key still on the old price: %v", got)
	}
	// Nothing was deleted or archived, and no price was edited.
	for _, c := range f.calls {
		if c.Method == "DELETE" || (c.Method == "POST" && strings.HasPrefix(c.Path, "/v1/prices/")) {
			t.Fatalf("destructive call: %+v", c)
		}
	}
}

func TestSyncDetectsEachKindOfPriceChange(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(map[string]any){
		"currency": func(p map[string]any) { p["currency"] = "eur" },
		"interval": func(p map[string]any) { p["recurring"] = map[string]any{"interval": "year", "interval_count": 1} },
		"count":    func(p map[string]any) { p["recurring"] = map[string]any{"interval": "month", "interval_count": 3} },
		"product":  func(p map[string]any) { p["product"] = "prod_somewhere_else" },
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := newFake(t)
			c := newClient(t, load(t, "subscription-tool.json"), srv)
			if _, err := c.Sync(ctx, false); err != nil {
				t.Fatal(err)
			}
			mutate(f.priceObjs["taskboard/plus/month"])
			r, err := c.Sync(ctx, true)
			if err != nil {
				t.Fatal(err)
			}
			if kinds(r)[stripe.SyncReplacePrice] != 1 {
				t.Fatalf("actions = %+v", r.Actions)
			}
		})
	}
}

func TestSyncUpdatesAProductNameAndMetadataButNothingElse(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	if _, err := c.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.products["payforge_taskboard_plus"]["name"] = "Old name"
	delete(f.products["payforge_taskboard_plus"]["metadata"].(map[string]any), "payforge_plan")
	r, err := c.Sync(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if kinds(r)[stripe.SyncUpdateProduct] != 1 {
		t.Fatalf("actions = %+v", r.Actions)
	}
	if f.products["payforge_taskboard_plus"]["name"] != "Plus" {
		t.Fatalf("name = %v", f.products["payforge_taskboard_plus"]["name"])
	}
	if f.products["payforge_taskboard_plus"]["metadata"].(map[string]any)["payforge_plan"] != "plus" {
		t.Fatal("metadata not restored")
	}
}

func TestSyncLeavesAnotherAppsObjectsAlone(t *testing.T) {
	ctx := context.Background()
	t.Run("product", func(t *testing.T) {
		f, srv := newFake(t)
		f.products["payforge_taskboard_plus"] = map[string]any{"id": "payforge_taskboard_plus", "object": "product", "name": "Plus", "active": true,
			"metadata": map[string]any{"payforge_app": "someone-else"}}
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(ctx, false)
		if !errors.Is(err, stripe.ErrForeign) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("price", func(t *testing.T) {
		f, srv := newFake(t)
		c := newClient(t, load(t, "subscription-tool.json"), srv)
		if _, err := c.Sync(ctx, false); err != nil {
			t.Fatal(err)
		}
		f.priceObjs["taskboard/plus/month"]["metadata"] = map[string]any{"payforge_app": "someone-else"}
		if _, err := c.Sync(ctx, false); !errors.Is(err, stripe.ErrForeign) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unmarked object is adopted", func(t *testing.T) {
		f, srv := newFake(t)
		f.products["payforge_taskboard_plus"] = map[string]any{"id": "payforge_taskboard_plus", "object": "product", "name": "Plus", "active": true,
			"metadata": map[string]any{}}
		r, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if kinds(r)[stripe.SyncUpdateProduct] != 1 {
			t.Fatalf("actions = %+v", r.Actions)
		}
	})
}

func TestSyncDoesNotRevive(t *testing.T) {
	f, srv := newFake(t)
	f.products["payforge_taskboard_plus"] = map[string]any{"id": "payforge_taskboard_plus", "object": "product", "name": "Plus", "active": false,
		"metadata": map[string]any{"payforge_app": "taskboard"}}
	_, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(context.Background(), false)
	if !errors.Is(err, stripe.ErrArchived) {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncReportsProgressWithTheError(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	f.fail["POST /v1/prices"] = 400
	r, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "create price taskboard/plus/month") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatal("the key is in the error")
	}
	if kinds(r)[stripe.SyncCreateProduct] != 2 {
		t.Fatalf("the products created before the failure are not reported: %+v", r.Actions)
	}
}

func TestSyncFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ fail, want string }{
		{"GET /v1/products/payforge_taskboard_plus", "read product"},
		{"POST /v1/products", "create product"},
		{"GET /v1/prices", "read price"},
	} {
		f, srv := newFake(t)
		f.fail[tc.fail] = 400
		if tc.want == "read price" {
			// Products must exist for the price step to be reached.
			for _, id := range []string{"payforge_taskboard_plus", "payforge_taskboard_business"} {
				name := map[string]string{"payforge_taskboard_plus": "Plus", "payforge_taskboard_business": "Business"}[id]
				f.products[id] = map[string]any{"id": id, "object": "product", "name": name, "active": true,
					"metadata": map[string]any{"payforge_app": "taskboard", "payforge_plan": strings.TrimPrefix(id, "payforge_taskboard_")}}
			}
		}
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(ctx, false)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", tc.fail, err, tc.want)
		}
	}
}

func TestProductIDIsDerivedFromTheKey(t *testing.T) {
	if got := stripe.ProductID("taskboard/plus"); got != "payforge_taskboard_plus" {
		t.Fatalf("ProductID = %q", got)
	}
}
