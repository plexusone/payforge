package stripe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/stripe"
)

func TestChangedCustomerDetailsInsideTheKeysDayDoNotFail(t *testing.T) {
	ctx := context.Background()
	_, srv := newFake(t) // the fake enforces idempotency as Stripe does
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	first := payforge.Subject{ID: "org-1", Email: "a@example.com"}
	second := payforge.Subject{ID: "org-1", Email: "b@example.com"}
	if _, err := c.EnsureCustomer(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Search has not caught up, so this creates again with the new email. Under a
	// key that ignored the email Stripe would refuse it.
	if _, err := c.EnsureCustomer(ctx, second); err != nil {
		t.Fatalf("a changed email: %v", err)
	}
	// The same details again replay the same customer.
	if _, err := c.EnsureCustomer(ctx, first); err != nil {
		t.Fatalf("a repeat: %v", err)
	}
}

func TestCreateAccountWithChangedDetailsInsideTheKeysDayDoesNotFail(t *testing.T) {
	ctx := context.Background()
	_, srv := newFake(t)
	c := newClient(t, load(t, "marketplace.json"), srv)
	if _, err := c.CreateAccount(ctx, payforge.Subject{ID: "s-1", Email: "a@example.com"}, "US"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateAccount(ctx, payforge.Subject{ID: "s-1", Email: "b@example.com"}, "US"); err != nil {
		t.Fatalf("a changed email: %v", err)
	}
}

func TestACheckoutThatFinishedIsNotHandedBackDead(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	f.prices["taskboard/plus/month"] = "price_plus_m"
	f.deadNext = true // the replay of a session the buyer already completed
	sess, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(ctx, planIn("plus"))
	if err != nil {
		t.Fatal(err)
	}
	if sess.URL == "" || sess.ID == "cs_test_dead" {
		t.Fatalf("session = %+v", sess)
	}
	if n := f.count("POST", "/v1/checkout/sessions"); n != 2 {
		t.Fatalf("%d sessions created", n)
	}
}

func TestACheckoutKeyIsNotReplayedAgainstAnotherPrice(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	f.prices["taskboard/plus/month"] = "price_old"
	if _, err := c.CheckoutPlan(ctx, planIn("plus")); err != nil {
		t.Fatal(err)
	}
	f.prices["taskboard/plus/month"] = "price_new" // a sync moved the lookup key
	if _, err := c.CheckoutPlan(ctx, planIn("plus")); err != nil {
		t.Fatalf("after the price moved: %v", err)
	}

	m := load(t, "marketplace.json")
	f.prices["craftmarket/sample_pack"] = "price_pack_old"
	f.ready(true)
	mc := newClient(t, m, srv)
	if _, err := mc.CheckoutSale(ctx, saleIn("sample_pack", 0)); err != nil {
		t.Fatal(err)
	}
	f.prices["craftmarket/sample_pack"] = "price_pack_new"
	if _, err := mc.CheckoutSale(ctx, saleIn("sample_pack", 0)); err != nil {
		t.Fatalf("a sale after the price moved: %v", err)
	}
	if _, err := mc.CheckoutProduct(ctx, payforge.ProductCheckout{Subject: subj, Product: "sample_pack",
		Reference: "o-1", SuccessURL: okURL, CancelURL: cancelURL}); err != nil {
		t.Fatal(err)
	}
	f.prices["craftmarket/sample_pack"] = "price_pack_newer"
	if _, err := mc.CheckoutProduct(ctx, payforge.ProductCheckout{Subject: subj, Product: "sample_pack",
		Reference: "o-1", SuccessURL: okURL, CancelURL: cancelURL}); err != nil {
		t.Fatalf("a purchase after the price moved: %v", err)
	}
}

func TestURLsAreHttpsUnlessLocalAndCanBeLimitedToHosts(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	f.prices["taskboard/plus/month"] = "price_plus_m"
	spec := load(t, "subscription-tool.json")
	open := newClient(t, spec, srv)
	limited, err := stripe.New(spec, stripe.Config{SecretKey: secretKey, APIURL: srv.URL, AllowedURLHosts: []string{"App.Example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	at := func(c *stripe.Client, success string) error {
		in := planIn("plus")
		in.SuccessURL = success
		_, err := c.CheckoutPlan(ctx, in)
		return err
	}
	for _, ok := range []string{"https://app.example.com/b", "http://localhost:3000/b", "http://127.0.0.1/b"} {
		if err := at(open, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://app.example.com/b", "https://user:pw@app.example.com/b", "ftp://app.example.com/b", "//app.example.com/b", "javascript:alert(1)"} {
		if err := at(open, bad); !errors.Is(err, payforge.ErrInvalidURL) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if err := at(limited, "https://app.example.com/b"); err != nil {
		t.Errorf("an allowed host, in another case: %v", err)
	}
	for _, bad := range []string{"https://evil.example.net/b", "https://app.example.com.evil.net/b", "https://app.example.com@evil.net/b"} {
		if err := at(limited, bad); !errors.Is(err, payforge.ErrInvalidURL) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestTheSessionPlaceholderGoesBeforeAFragment(t *testing.T) {
	f, srv := newFake(t)
	f.prices["taskboard/business/month"] = "price_plus_m"
	in := planIn("business")
	in.SuccessURL = "https://app.example.com/billing?x=1#done"
	if _, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got := f.last("POST", "/v1/checkout/sessions").Form["success_url"]
	if got != "https://app.example.com/billing?x=1&session_id={CHECKOUT_SESSION_ID}#done" {
		t.Fatalf("success_url = %q", got)
	}
}

func TestASaleNeverPaysAnAccountThisAppDidNotCreate(t *testing.T) {
	ctx := context.Background()
	for name, meta := range map[string]map[string]any{
		"another app": {"payforge_app": "otherapp"},
		"no metadata": {},
	} {
		f, srv := newFake(t)
		f.ready(true)
		f.accounts[seller]["metadata"] = meta
		_, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutSale(ctx, saleIn("listing", 1000))
		if !errors.Is(err, payforge.ErrForeignAccount) {
			t.Errorf("%s: err = %v", name, err)
		}
		if n := f.count("POST", "/v1/checkout/sessions"); n != 0 {
			t.Errorf("%s: %d checkouts made", name, n)
		}
	}
}

func TestSyncDoesNotReplayAnOldPriceWhenTheAmountGoesBack(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	spec := load(t, "subscription-tool.json")
	amount := func(cents int64) {
		for i := range spec.Plans {
			if spec.Plans[i].ID == "plus" {
				for j := range spec.Plans[i].Prices {
					if spec.Plans[i].Prices[j].Interval == "month" {
						spec.Plans[i].Prices[j].AmountCents = cents
					}
				}
			}
		}
	}
	c := newClient(t, spec, srv)
	for _, cents := range []int64{1200, 1500, 1200} { // A, then B, then A again inside a day
		amount(cents)
		if _, err := c.Sync(ctx, false); err != nil {
			t.Fatalf("%d: %v", cents, err)
		}
		if got := f.priceObjs["taskboard/plus/month"]["unit_amount"]; got != cents {
			t.Fatalf("after syncing %d the key holds %v", cents, got)
		}
	}
}

func TestSyncSaysWhenAProductWasDeleted(t *testing.T) {
	f, srv := newFake(t)
	f.products["payforge_taskboard_plus"] = map[string]any{"id": "payforge_taskboard_plus", "object": "product", "deleted": true}
	_, err := newClient(t, load(t, "subscription-tool.json"), srv).Sync(context.Background(), true)
	if !errors.Is(err, stripe.ErrDeleted) || strings.Contains(err.Error(), "unarchive") {
		t.Fatalf("err = %v", err)
	}
}
