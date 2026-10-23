package stripe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/stripe"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

var subj = payforge.Subject{ID: "org-1", Name: "Acme", Email: "billing@example.com"}

const (
	okURL     = "https://app.example.com/billing"
	cancelURL = "https://app.example.com/pricing"
)

func planIn(plan string) payforge.PlanCheckout {
	return payforge.PlanCheckout{Subject: subj, Plan: plan, Interval: pricingspec.IntervalMonth, SuccessURL: okURL, CancelURL: cancelURL}
}

func TestNewRequiresAKeyAndAValidSpec(t *testing.T) {
	spec := load(t, "subscription-tool.json")
	if _, err := stripe.New(spec, stripe.Config{}); !errors.Is(err, payforge.ErrNotConfigured) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := stripe.New(nil, stripe.Config{SecretKey: secretKey}); err == nil {
		t.Fatal("nil spec accepted")
	}
	bad := *spec
	bad.DefaultPlan = "nope"
	if _, err := stripe.New(&bad, stripe.Config{SecretKey: secretKey}); err == nil || strings.Contains(err.Error(), secretKey) {
		t.Fatalf("invalid spec: %v", err)
	}
}

func TestEnabledAndPlan(t *testing.T) {
	_, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	if !c.Enabled() {
		t.Fatal("not enabled")
	}
	if got := c.Plan("plus").ID; got != "plus" {
		t.Fatalf("plus = %q", got)
	}
	if got := c.Plan("gone").ID; got != "free" {
		t.Fatalf("unknown plan = %q, want the default", got)
	}
}

func TestEnsureCustomer(t *testing.T) {
	ctx := context.Background()
	t.Run("an existing customer is found, not created", func(t *testing.T) {
		f, srv := newFake(t)
		f.customers = []string{"cus_existing"}
		id, err := newClient(t, load(t, "subscription-tool.json"), srv).EnsureCustomer(ctx, subj)
		if err != nil || id != "cus_existing" {
			t.Fatalf("id = %q, err = %v", id, err)
		}
		if f.count("POST", "/v1/customers") != 0 {
			t.Fatal("created a second customer")
		}
		q := f.last("GET", "/v1/customers/search").Form["query"]
		if !strings.Contains(q, "payforge_app']:'taskboard'") || !strings.Contains(q, "payforge_subject']:'org-1'") {
			t.Fatalf("query = %q", q)
		}
	})
	t.Run("a new customer carries the app and subject, with an idempotency key", func(t *testing.T) {
		f, srv := newFake(t)
		id, err := newClient(t, load(t, "subscription-tool.json"), srv).EnsureCustomer(ctx, subj)
		if err != nil || id != "cus_new" {
			t.Fatalf("id = %q, err = %v", id, err)
		}
		c := f.last("POST", "/v1/customers")
		if c.Form["metadata[payforge_app]"] != "taskboard" || c.Form["metadata[payforge_subject]"] != "org-1" {
			t.Fatalf("metadata: %v", c.Form)
		}
		if c.Form["name"] != "Acme" || c.Form["email"] != "billing@example.com" {
			t.Fatalf("form: %v", c.Form)
		}
		if !strings.HasPrefix(c.IdemKey, "customer-") {
			t.Fatalf("idempotency key = %q", c.IdemKey)
		}
		if c.Auth != "Bearer "+secretKey {
			t.Fatalf("auth header = %q", c.Auth)
		}
	})
	t.Run("an ID that could break a query is refused before any request", func(t *testing.T) {
		f, srv := newFake(t)
		c := newClient(t, load(t, "subscription-tool.json"), srv)
		for _, id := range []string{"", "a' OR metadata['x']:'y", "has space", "-lead", strings.Repeat("a", 129)} {
			if _, err := c.EnsureCustomer(ctx, payforge.Subject{ID: id}); !errors.Is(err, payforge.ErrInvalidSubject) {
				t.Errorf("%q: %v", id, err)
			}
		}
		if len(f.calls) != 0 {
			t.Fatalf("requests were made: %+v", f.calls)
		}
	})
}

func TestCheckoutPlan(t *testing.T) {
	ctx := context.Background()
	in := payforge.PlanCheckout{Subject: subj, Plan: "plus", Interval: pricingspec.IntervalMonth, SuccessURL: okURL, CancelURL: cancelURL}

	t.Run("a trial plan starts without a card", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["taskboard/plus/month"] = "price_plus_m"
		sess, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if sess.ID != "cs_test_1" || sess.URL != "https://checkout.example/cs_test_1" {
			t.Fatalf("session = %+v", sess)
		}
		c := f.last("POST", "/v1/checkout/sessions")
		want := map[string]string{
			"mode":                                 "subscription",
			"customer":                             "cus_new",
			"client_reference_id":                  "org-1",
			"line_items[0][price]":                 "price_plus_m",
			"line_items[0][quantity]":              "1",
			"cancel_url":                           cancelURL,
			"success_url":                          okURL + "?session_id={CHECKOUT_SESSION_ID}",
			"metadata[payforge_app]":               "taskboard",
			"metadata[payforge_plan]":              "plus",
			"metadata[payforge_subject]":           "org-1",
			"payment_method_collection":            "if_required",
			"subscription_data[trial_period_days]": "14",
			"subscription_data[trial_settings][end_behavior][missing_payment_method]": "cancel",
			"subscription_data[metadata][payforge_app]":                               "taskboard",
			"subscription_data[metadata][payforge_plan]":                              "plus",
			"subscription_data[metadata][payforge_subject]":                           "org-1",
		}
		for k, v := range want {
			if c.Form[k] != v {
				t.Errorf("%s = %q, want %q", k, c.Form[k], v)
			}
		}
		if c.IdemKey == "" {
			t.Error("no idempotency key")
		}
	})
	t.Run("the same request reuses its idempotency key and a different URL does not", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["taskboard/plus/month"] = "price_plus_m"
		c := newClient(t, load(t, "subscription-tool.json"), srv)
		if _, err := c.CheckoutPlan(ctx, in); err != nil {
			t.Fatal(err)
		}
		a := f.last("POST", "/v1/checkout/sessions").IdemKey
		if _, err := c.CheckoutPlan(ctx, in); err != nil {
			t.Fatal(err)
		}
		if b := f.last("POST", "/v1/checkout/sessions").IdemKey; a != b {
			t.Fatalf("same request, keys %q and %q", a, b)
		}
		other := in
		other.CancelURL = "https://app.example.com/elsewhere"
		if _, err := c.CheckoutPlan(ctx, other); err != nil {
			t.Fatal(err)
		}
		if b := f.last("POST", "/v1/checkout/sessions").IdemKey; a == b {
			t.Fatal("a different cancel URL got the same key")
		}
	})
	t.Run("a plan without a trial asks for a card", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["craftmarket/seller/month"] = "price_seller_m"
		c := newClient(t, load(t, "marketplace.json"), srv)
		_, err := c.CheckoutPlan(ctx, payforge.PlanCheckout{Subject: subj, Plan: "seller", Interval: pricingspec.IntervalMonth, SuccessURL: okURL + "?x=1", CancelURL: cancelURL})
		if err != nil {
			t.Fatal(err)
		}
		form := f.last("POST", "/v1/checkout/sessions").Form
		if _, ok := form["payment_method_collection"]; ok {
			t.Error("payment_method_collection set without a trial")
		}
		if _, ok := form["subscription_data[trial_period_days]"]; ok {
			t.Error("trial set on a plan without one")
		}
		if form["success_url"] != okURL+"?x=1&session_id={CHECKOUT_SESSION_ID}" {
			t.Errorf("success_url = %q", form["success_url"])
		}
	})
	t.Run("a granting subscription blocks a second checkout", func(t *testing.T) {
		for status, blocked := range map[string]bool{
			"active": true, "trialing": true, "past_due": true,
			"canceled": false, "unpaid": false, "incomplete_expired": false,
		} {
			f, srv := newFake(t)
			f.prices["taskboard/plus/month"] = "price_plus_m"
			f.subscriptions = []string{status}
			_, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(ctx, in)
			if blocked != errors.Is(err, payforge.ErrAlreadySubscribed) {
				t.Errorf("%s: err = %v, blocked = %v", status, err, blocked)
			}
			if blocked && f.count("POST", "/v1/checkout/sessions") != 0 {
				t.Errorf("%s: a session was created", status)
			}
		}
	})
	t.Run("what cannot be bought is refused before any request", func(t *testing.T) {
		f, srv := newFake(t)
		c := newClient(t, load(t, "subscription-tool.json"), srv)
		cases := map[string]struct {
			mut  func(*payforge.PlanCheckout)
			want error
		}{
			"unknown plan":     {func(p *payforge.PlanCheckout) { p.Plan = "gone" }, payforge.ErrUnknownPlan},
			"arranged plan":    {func(p *payforge.PlanCheckout) { p.Plan = "enterprise" }, payforge.ErrNotSelfServe},
			"free plan":        {func(p *payforge.PlanCheckout) { p.Plan = "free" }, payforge.ErrNoPrice},
			"no such interval": {func(p *payforge.PlanCheckout) { p.Interval = "week" }, payforge.ErrNoPrice},
			"bad success url":  {func(p *payforge.PlanCheckout) { p.SuccessURL = "javascript:alert(1)" }, payforge.ErrInvalidURL},
			"relative cancel":  {func(p *payforge.PlanCheckout) { p.CancelURL = "/pricing" }, payforge.ErrInvalidURL},
			"bad subject":      {func(p *payforge.PlanCheckout) { p.Subject.ID = "" }, payforge.ErrInvalidSubject},
		}
		for name, tc := range cases {
			x := in
			tc.mut(&x)
			if _, err := c.CheckoutPlan(ctx, x); !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", name, err, tc.want)
			}
		}
		if len(f.calls) != 0 {
			t.Fatalf("requests were made: %+v", f.calls)
		}
	})
	t.Run("an unsynced catalog says to sync", func(t *testing.T) {
		_, srv := newFake(t)
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(ctx, in)
		if !errors.Is(err, payforge.ErrNotSynced) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a Stripe error is wrapped without the key", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["taskboard/plus/month"] = "price_plus_m"
		f.fail["POST /v1/checkout/sessions"] = 400
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutPlan(ctx, in)
		if err == nil || !strings.Contains(err.Error(), "create plan checkout") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), secretKey) {
			t.Fatalf("the key is in the error: %v", err)
		}
	})
}

func TestCheckoutProduct(t *testing.T) {
	ctx := context.Background()
	t.Run("a fixed product is sold at its synced price", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["craftmarket/sample_pack"] = "price_pack"
		sess, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutProduct(ctx, payforge.ProductCheckout{
			Subject: subj, Product: "sample_pack", Reference: "purchase-9", SuccessURL: okURL, CancelURL: cancelURL,
		})
		if err != nil || sess.ID != "cs_test_1" {
			t.Fatalf("session = %+v, err = %v", sess, err)
		}
		c := f.last("POST", "/v1/checkout/sessions")
		for k, v := range map[string]string{
			"mode": "payment", "line_items[0][price]": "price_pack",
			"metadata[payforge_app]": "craftmarket", "metadata[payforge_product]": "sample_pack",
			"payment_intent_data[metadata][payforge_product]": "sample_pack",
			"payment_intent_data[metadata][payforge_subject]": "org-1",
		} {
			if c.Form[k] != v {
				t.Errorf("%s = %q, want %q", k, c.Form[k], v)
			}
		}
		if c.IdemKey == "" {
			t.Error("a reference should give an idempotency key")
		}
	})
	t.Run("a reference makes a retry the same request; without one each call is its own", func(t *testing.T) {
		f, srv := newFake(t)
		f.prices["craftmarket/sample_pack"] = "price_pack"
		c := newClient(t, load(t, "marketplace.json"), srv)
		buy := func(ref string) string {
			t.Helper()
			_, err := c.CheckoutProduct(ctx, payforge.ProductCheckout{
				Subject: subj, Product: "sample_pack", Reference: ref, SuccessURL: okURL, CancelURL: cancelURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return f.last("POST", "/v1/checkout/sessions").IdemKey
		}
		if a, b := buy("purchase-9"), buy("purchase-9"); a != b {
			t.Errorf("same reference, keys %q and %q", a, b)
		}
		if a, b := buy("purchase-9"), buy("purchase-10"); a == b {
			t.Error("different references share a key")
		}
		// The library gives every creating call a key of its own.
		if a, b := buy(""), buy(""); a == "" || a == b {
			t.Errorf("without a reference, keys %q and %q", a, b)
		}
	})
	t.Run("a seller-set product is sold at the chosen amount", func(t *testing.T) {
		f, srv := newFake(t)
		_, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutProduct(ctx, payforge.ProductCheckout{
			Subject: subj, Product: "listing", AmountCents: 2500, SuccessURL: okURL, CancelURL: cancelURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		c := f.last("POST", "/v1/checkout/sessions")
		for k, v := range map[string]string{
			"line_items[0][price_data][currency]":           "usd",
			"line_items[0][price_data][unit_amount]":        "2500",
			"line_items[0][price_data][product_data][name]": "A seller's listing",
		} {
			if c.Form[k] != v {
				t.Errorf("%s = %q, want %q", k, c.Form[k], v)
			}
		}
		if f.count("GET", "/v1/prices") != 0 {
			t.Error("a seller-set product needs no synced price")
		}
	})
	t.Run("amounts are checked before any request", func(t *testing.T) {
		f, srv := newFake(t)
		c := newClient(t, load(t, "marketplace.json"), srv)
		base := payforge.ProductCheckout{Subject: subj, SuccessURL: okURL, CancelURL: cancelURL}
		for name, tc := range map[string]struct {
			product string
			amount  int64
			want    error
		}{
			"under the minimum":    {"listing", 99, payforge.ErrBelowMinimum},
			"an amount on a fixed": {"sample_pack", 100, payforge.ErrAmountNotAllowed},
			"unknown product":      {"gone", 0, payforge.ErrUnknownProduct},
		} {
			in := base
			in.Product, in.AmountCents = tc.product, tc.amount
			if _, err := c.CheckoutProduct(ctx, in); !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", name, err, tc.want)
			}
		}
		if len(f.calls) != 0 {
			t.Fatalf("requests were made: %+v", f.calls)
		}
	})
	t.Run("exactly the minimum is allowed", func(t *testing.T) {
		_, srv := newFake(t)
		_, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutProduct(ctx, payforge.ProductCheckout{
			Subject: subj, Product: "listing", AmountCents: 100, SuccessURL: okURL, CancelURL: cancelURL,
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestPortal(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	url, err := c.Portal(ctx, "cus_1", okURL)
	if err != nil || url != "https://portal.example/bps_1" {
		t.Fatalf("url = %q, err = %v", url, err)
	}
	form := f.last("POST", "/v1/billing_portal/sessions").Form
	if form["customer"] != "cus_1" || form["return_url"] != okURL {
		t.Fatalf("form = %v", form)
	}
	if _, err := c.Portal(ctx, "", okURL); !errors.Is(err, payforge.ErrInvalidSubject) {
		t.Errorf("no customer: %v", err)
	}
	if _, err := c.Portal(ctx, "cus_1", "/back"); !errors.Is(err, payforge.ErrInvalidURL) {
		t.Errorf("relative URL: %v", err)
	}
}

func TestStripeFailuresAreReportedNotSwallowed(t *testing.T) {
	ctx := context.Background()
	plan := payforge.PlanCheckout{Subject: subj, Plan: "plus", Interval: pricingspec.IntervalMonth, SuccessURL: okURL, CancelURL: cancelURL}
	product := payforge.ProductCheckout{Subject: subj, Product: "sample_pack", SuccessURL: okURL, CancelURL: cancelURL}
	for _, tc := range []struct {
		name, fail, want string
		run              func(*stripe.Client) error
	}{
		{"price lookup", "GET /v1/prices", "look up price", func(c *stripe.Client) error { _, err := c.CheckoutPlan(ctx, plan); return err }},
		{"customer search", "GET /v1/customers/search", "search customers", func(c *stripe.Client) error { _, err := c.CheckoutPlan(ctx, plan); return err }},
		{"customer create", "POST /v1/customers", "create customer", func(c *stripe.Client) error { _, err := c.EnsureCustomer(ctx, subj); return err }},
		{"subscription list", "GET /v1/subscriptions", "list subscriptions", func(c *stripe.Client) error { _, err := c.CheckoutPlan(ctx, plan); return err }},
		{"product checkout", "POST /v1/checkout/sessions", "create product checkout", func(c *stripe.Client) error { _, err := c.CheckoutProduct(ctx, product); return err }},
		{"product price lookup", "GET /v1/prices", "look up price", func(c *stripe.Client) error { _, err := c.CheckoutProduct(ctx, product); return err }},
		{"portal", "POST /v1/billing_portal/sessions", "create portal session", func(c *stripe.Client) error { _, err := c.Portal(ctx, "cus_1", okURL); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFake(t)
			f.prices["taskboard/plus/month"] = "price_plus_m"
			f.prices["craftmarket/sample_pack"] = "price_pack"
			f.fail[tc.fail] = 400
			spec := "subscription-tool.json"
			if strings.HasPrefix(tc.name, "product") {
				spec = "marketplace.json"
			}
			err := tc.run(newClient(t, load(t, spec), srv))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), secretKey) {
				t.Fatalf("the key is in the error: %v", err)
			}
		})
	}
}

func TestSubscriptionsReadsTheCustomersLiveSubscriptions(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	f.subscriptionObjs = []map[string]any{{
		"id": "sub_1", "object": "subscription", "status": "past_due", "cancel_at_period_end": true, "trial_end": 1791000000,
		"metadata": map[string]any{"payforge_plan": "plus"},
		"items": map[string]any{"object": "list", "data": []map[string]any{
			{"id": "si_1", "price": map[string]any{"id": "price_a", "lookup_key": "taskboard/plus/month"}, "current_period_end": 1795000000},
			{"id": "si_2", "price": map[string]any{"id": "price_b", "lookup_key": "taskboard/business/month"}, "current_period_end": 1796000000},
		}},
	}}
	c := newClient(t, load(t, "subscription-tool.json"), srv)
	got, err := c.Subscriptions(ctx, "cus_1")
	if err != nil || len(got) != 1 {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	s := got[0]
	if s.ID != "sub_1" || s.Status != "past_due" || !s.CancelAtPeriodEnd || s.Metadata["payforge_plan"] != "plus" {
		t.Fatalf("subscription = %+v", s)
	}
	if len(s.Items) != 2 || s.Items[0].LookupKey != "taskboard/plus/month" || s.Items[1].PriceID != "price_b" {
		t.Fatalf("items = %+v", s.Items)
	}
	if s.PeriodEnd.Unix() != 1796000000 || s.TrialEnd.Unix() != 1791000000 {
		t.Fatalf("times = %v %v", s.PeriodEnd, s.TrialEnd)
	}
	if f.last("GET", "/v1/subscriptions").Form["customer"] != "cus_1" {
		t.Fatalf("customer = %v", f.last("GET", "/v1/subscriptions").Form)
	}

	if _, err := c.Subscriptions(ctx, ""); !errors.Is(err, payforge.ErrInvalidSubject) {
		t.Errorf("no customer: %v", err)
	}
	f.fail["GET /v1/subscriptions"] = 400
	if _, err := c.Subscriptions(ctx, "cus_1"); err == nil || !strings.Contains(err.Error(), "list subscriptions") || strings.Contains(err.Error(), secretKey) {
		t.Errorf("failure: %v", err)
	}
}
