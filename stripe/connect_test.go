package stripe_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/plexusone/payforge"
)

const seller = "acct_seller123"

// ready registers the test seller that can take charges.
func (f *fake) ready(charges bool) {
	f.accounts[seller] = map[string]any{"id": seller, "object": "account", "charges_enabled": charges,
		"payouts_enabled": charges, "details_submitted": charges,
		"metadata": map[string]any{"payforge_app": "craftmarket", "payforge_subject": "seller-1"}}
}

func saleIn(product string, amount int64) payforge.Sale {
	return payforge.Sale{Buyer: subj, SellerAccount: seller, SellerPlan: "seller", Product: product,
		AmountCents: amount, Reference: "order-1", SuccessURL: okURL, CancelURL: cancelURL}
}

func TestCreateAccount(t *testing.T) {
	ctx := context.Background()
	t.Run("an Express account is requested with the seller named in metadata", func(t *testing.T) {
		f, srv := newFake(t)
		id, err := newClient(t, load(t, "marketplace.json"), srv).CreateAccount(ctx, subj, "US")
		if err != nil || id != "acct_new123" {
			t.Fatalf("id = %q, err = %v", id, err)
		}
		c := f.last("POST", "/v1/accounts")
		for k, want := range map[string]string{
			"type": "express", "country": "US", "email": subj.Email,
			"metadata[payforge_app]": "craftmarket", "metadata[payforge_subject]": "org-1",
			"capabilities[card_payments][requested]": "true", "capabilities[transfers][requested]": "true",
		} {
			if c.Form[k] != want {
				t.Errorf("%s = %q, want %q", k, c.Form[k], want)
			}
		}
		if !strings.HasPrefix(c.IdemKey, "connect-account-") {
			t.Errorf("idempotency key = %q", c.IdemKey)
		}
	})
	t.Run("bad input never reaches Stripe", func(t *testing.T) {
		f, srv := newFake(t)
		cl := newClient(t, load(t, "marketplace.json"), srv)
		if _, err := cl.CreateAccount(ctx, payforge.Subject{}, "US"); !errors.Is(err, payforge.ErrInvalidSubject) {
			t.Errorf("no subject: %v", err)
		}
		for _, country := range []string{"", "us", "USA"} {
			if _, err := cl.CreateAccount(ctx, subj, country); !errors.Is(err, payforge.ErrInvalidAccount) {
				t.Errorf("country %q: %v", country, err)
			}
		}
		if n := f.count("POST", "/v1/accounts"); n != 0 {
			t.Errorf("%d requests made", n)
		}
	})
	t.Run("a spec without a marketplace has no sellers", func(t *testing.T) {
		_, srv := newFake(t)
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).CreateAccount(ctx, subj, "US")
		if !errors.Is(err, payforge.ErrNoMarketplace) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a Stripe failure is returned without the key", func(t *testing.T) {
		f, srv := newFake(t)
		f.fail["POST /v1/accounts"] = 400
		_, err := newClient(t, load(t, "marketplace.json"), srv).CreateAccount(ctx, subj, "US")
		if err == nil || strings.Contains(err.Error(), secretKey) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOnboardingLink(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	cl := newClient(t, load(t, "marketplace.json"), srv)
	url, err := cl.OnboardingLink(ctx, seller, okURL, cancelURL)
	if err != nil || url != "https://connect.example/onboard/1" {
		t.Fatalf("url = %q, err = %v", url, err)
	}
	c := f.last("POST", "/v1/account_links")
	for k, want := range map[string]string{"account": seller, "type": "account_onboarding", "refresh_url": okURL, "return_url": cancelURL} {
		if c.Form[k] != want {
			t.Errorf("%s = %q, want %q", k, c.Form[k], want)
		}
	}
	if _, err := cl.OnboardingLink(ctx, "../accounts", okURL, cancelURL); !errors.Is(err, payforge.ErrInvalidAccount) {
		t.Errorf("malformed account: %v", err)
	}
	if _, err := cl.OnboardingLink(ctx, seller, "javascript:alert(1)", cancelURL); !errors.Is(err, payforge.ErrInvalidURL) {
		t.Errorf("bad refresh URL: %v", err)
	}
	if _, err := cl.OnboardingLink(ctx, seller, okURL, "/relative"); !errors.Is(err, payforge.ErrInvalidURL) {
		t.Errorf("bad return URL: %v", err)
	}
}

func TestAccount(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t)
	f.ready(true)
	f.accounts["acct_half123"] = map[string]any{"id": "acct_half123", "object": "account", "details_submitted": true}
	cl := newClient(t, load(t, "marketplace.json"), srv)
	got, err := cl.Account(ctx, seller)
	if err != nil || got != (payforge.AccountState{ID: seller, ChargesEnabled: true, PayoutsEnabled: true, DetailsSubmitted: true}) {
		t.Fatalf("ready = %+v, err = %v", got, err)
	}
	got, err = cl.Account(ctx, "acct_half123")
	if err != nil || got.ChargesEnabled || got.PayoutsEnabled || !got.DetailsSubmitted {
		t.Fatalf("half = %+v, err = %v", got, err)
	}
	if _, err := cl.Account(ctx, "acct_gone1234"); err == nil {
		t.Error("a missing account was read as fine")
	}
}

func TestCheckoutSale(t *testing.T) {
	ctx := context.Background()
	t.Run("a destination charge on the seller's behalf with the plan's fee", func(t *testing.T) {
		f, srv := newFake(t)
		f.ready(true)
		sess, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutSale(ctx, saleIn("listing", 2599))
		if err != nil || sess.ID != "cs_test_1" {
			t.Fatalf("sess = %+v, err = %v", sess, err)
		}
		c := f.last("POST", "/v1/checkout/sessions")
		// seller plan is 800 bps: 2599 * 8% = 207.92, rounded down.
		for k, want := range map[string]string{
			"mode":                              "payment",
			"payment_intent_data[on_behalf_of]": seller,
			"payment_intent_data[transfer_data][destination]": seller,
			"payment_intent_data[application_fee_amount]":     "207",
			"payment_intent_data[metadata][payforge_seller]":  seller,
			"payment_intent_data[metadata][payforge_product]": "listing",
			"metadata[payforge_seller]":                       seller,
			"line_items[0][price_data][unit_amount]":          "2599",
		} {
			if c.Form[k] != want {
				t.Errorf("%s = %q, want %q", k, c.Form[k], want)
			}
		}
		if !strings.HasPrefix(c.IdemKey, "sale-") {
			t.Errorf("idempotency key = %q", c.IdemKey)
		}
	})
	t.Run("the fee follows the seller's plan, then the marketplace", func(t *testing.T) {
		// 499 cents at 800, 500 and 1000 bps is 39.92, 24.95 and 49.9: rounded down.
		for _, tc := range []struct{ plan, want string }{
			{"seller", "39"}, {"pro_seller", "24"}, {"browser", "49"}, {"", "49"}, {"unknown", "49"},
		} {
			f, srv := newFake(t)
			f.ready(true)
			f.prices["craftmarket/sample_pack"] = "price_pack"
			in := saleIn("sample_pack", 0)
			in.SellerPlan = tc.plan
			if _, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutSale(ctx, in); err != nil {
				t.Fatalf("%q: %v", tc.plan, err)
			}
			c := f.last("POST", "/v1/checkout/sessions")
			if got := c.Form["payment_intent_data[application_fee_amount]"]; got != tc.want {
				t.Errorf("plan %q: fee = %q, want %q", tc.plan, got, tc.want)
			}
			if c.Form["line_items[0][price]"] != "price_pack" {
				t.Errorf("plan %q: price = %q", tc.plan, c.Form["line_items[0][price]"])
			}
		}
	})
	t.Run("a zero fee sends no application fee", func(t *testing.T) {
		f, srv := newFake(t)
		f.ready(true)
		spec := load(t, "marketplace.json")
		zero := 0
		for i := range spec.Plans {
			if spec.Plans[i].ID == "seller" {
				spec.Plans[i].PlatformFeeBps = &zero
			}
		}
		if _, err := newClient(t, spec, srv).CheckoutSale(ctx, saleIn("listing", 1000)); err != nil {
			t.Fatal(err)
		}
		if v, ok := f.last("POST", "/v1/checkout/sessions").Form["payment_intent_data[application_fee_amount]"]; ok {
			t.Errorf("application_fee_amount = %q", v)
		}
	})
	t.Run("a seller who cannot take charges is refused before any checkout", func(t *testing.T) {
		f, srv := newFake(t)
		f.ready(false)
		_, err := newClient(t, load(t, "marketplace.json"), srv).CheckoutSale(ctx, saleIn("listing", 1000))
		if !errors.Is(err, payforge.ErrSellerNotReady) {
			t.Fatalf("err = %v", err)
		}
		if n := f.count("POST", "/v1/checkout/sessions"); n != 0 {
			t.Errorf("%d checkouts made", n)
		}
	})
	t.Run("invalid sales never reach Stripe", func(t *testing.T) {
		f, srv := newFake(t)
		f.ready(true)
		cl := newClient(t, load(t, "marketplace.json"), srv)
		bad := func(mod func(*payforge.Sale)) payforge.Sale { s := saleIn("listing", 1000); mod(&s); return s }
		for name, tc := range map[string]struct {
			in   payforge.Sale
			want error
		}{
			"no buyer":                {bad(func(s *payforge.Sale) { s.Buyer = payforge.Subject{} }), payforge.ErrInvalidSubject},
			"malformed seller":        {bad(func(s *payforge.Sale) { s.SellerAccount = "seller" }), payforge.ErrInvalidAccount},
			"bad success URL":         {bad(func(s *payforge.Sale) { s.SuccessURL = "x" }), payforge.ErrInvalidURL},
			"unknown product":         {bad(func(s *payforge.Sale) { s.Product = "nope" }), payforge.ErrUnknownProduct},
			"under the minimum":       {bad(func(s *payforge.Sale) { s.AmountCents = 99 }), payforge.ErrBelowMinimum},
			"amount on a fixed price": {saleIn("sample_pack", 100), payforge.ErrAmountNotAllowed},
		} {
			if _, err := cl.CheckoutSale(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", name, err, tc.want)
			}
		}
		if n := f.count("POST", "/v1/checkout/sessions"); n != 0 {
			t.Errorf("%d checkouts made", n)
		}
	})
	t.Run("a spec without a marketplace cannot sell", func(t *testing.T) {
		_, srv := newFake(t)
		_, err := newClient(t, load(t, "subscription-tool.json"), srv).CheckoutSale(ctx, saleIn("listing", 1000))
		if !errors.Is(err, payforge.ErrNoMarketplace) {
			t.Fatalf("err = %v", err)
		}
	})
}
