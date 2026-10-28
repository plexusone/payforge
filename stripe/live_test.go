package stripe_test

// The live test runs payforge against Stripe's TEST mode. It is opt-in: it does
// nothing unless PAYFORGE_LIVE_STRIPE_KEY is set, and it refuses any key that is
// not a test-mode secret key. It uses an app name made up for the run, so it
// never touches objects it did not create, and it removes or archives what it
// made when it ends.
//
//	PAYFORGE_LIVE_STRIPE_KEY=sk_test_... go test ./stripe -run Live -v
//
// It settles what the stand-in server in the other tests can only assume. Lines
// that begin with OBSERVED are behaviors it records rather than asserts.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	stripesdk "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/stripe"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

const liveSecret = "whsec_live_test_only" //nolint:gosec // G101: signs payloads this test builds itself

type live struct {
	t     *testing.T
	ctx   context.Context
	key   string
	sc    *stripesdk.Client
	start time.Time
}

func liveKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("PAYFORGE_LIVE_STRIPE_KEY")
	if key == "" {
		t.Skip("PAYFORGE_LIVE_STRIPE_KEY is not set; the live test talks to Stripe's test mode")
	}
	if !strings.HasPrefix(key, "sk_test_") {
		// Loud on purpose: a live key in this variable must never be used.
		t.Fatal("PAYFORGE_LIVE_STRIPE_KEY must be a test-mode secret key (sk_test_...)")
	}
	return key
}

// freshSpec loads a test spec under an app name made up for this run.
func freshSpec(t *testing.T, name string) *pricingspec.Spec {
	t.Helper()
	spec := load(t, name)
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	spec.App = "pfl" + hex.EncodeToString(b[:])
	return spec
}

func (l *live) must(err error, what string) {
	l.t.Helper()
	if err != nil {
		l.t.Fatalf("%s: %v", what, err)
	}
}

func (l *live) observe(format string, args ...any) {
	l.t.Logf("OBSERVED "+format, args...)
}

// priceByKey reads the active price holding a lookup key.
func (l *live) priceByKey(key string) *stripesdk.Price {
	l.t.Helper()
	for p, err := range l.sc.V1Prices.List(l.ctx, &stripesdk.PriceListParams{
		Active: stripesdk.Bool(true), LookupKeys: []*string{stripesdk.String(key)},
	}).All(l.ctx) {
		l.must(err, "list prices")
		return p
	}
	return nil
}

// archive deactivates what a spec created: Stripe does not delete prices, and a
// product with prices cannot be deleted. Failures are logged, not hidden.
func (l *live) archive(spec *pricingspec.Spec) {
	plan := payforge.ProviderPlanFor(spec)
	for _, pr := range plan.Prices {
		if p := l.priceByKey(pr.LookupKey); p != nil {
			if _, err := l.sc.V1Prices.Update(l.ctx, p.ID, &stripesdk.PriceUpdateParams{Active: stripesdk.Bool(false)}); err != nil {
				l.t.Logf("cleanup: archive price %s: %v", pr.LookupKey, err)
			}
		}
	}
	for _, pk := range plan.Products {
		id := stripe.ProductID(pk.Key)
		if _, err := l.sc.V1Products.Update(l.ctx, id, &stripesdk.ProductUpdateParams{Active: stripesdk.Bool(false)}); err != nil {
			l.t.Logf("cleanup: archive product %s: %v", id, err)
		}
	}
}

// waitEvent polls for an event of a type about a customer or account, since
// Stripe makes them a moment after the change.
func (l *live) waitEvent(typ, owner string) json.RawMessage {
	l.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for ev, err := range l.sc.V1Events.List(l.ctx, &stripesdk.EventListParams{
			Type:         stripesdk.String(typ),
			CreatedRange: &stripesdk.RangeQueryParams{GreaterThanOrEqual: l.start.Unix() - 5},
		}).All(l.ctx) {
			l.must(err, "list events")
			var o struct {
				Customer string `json:"customer"`
				ID       string `json:"id"`
			}
			if ev.Data == nil || json.Unmarshal(ev.Data.Raw, &o) != nil {
				continue
			}
			if o.Customer == owner || o.ID == owner {
				return json.RawMessage(fmt.Sprintf(`{"id":%q,"object":"event","created":%d,"livemode":false,"type":%q,"data":{"object":%s}}`,
					ev.ID, ev.Created, ev.Type, ev.Data.Raw))
			}
		}
		time.Sleep(2 * time.Second)
	}
	l.t.Fatalf("no %s event for %s within 30s", typ, owner)
	return nil
}

// signed turns an event Stripe made into a request the webhook reader verifies.
func signed(payload []byte) (string, stripe.Webhook) {
	at := time.Now()
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(webhook.ComputeSignature(at, payload, liveSecret))),
		stripe.Webhook{Mode: stripe.ModeTest, Secrets: []string{liveSecret}}
}

func TestLiveSubscriptions(t *testing.T) {
	key := liveKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	l := &live{t: t, ctx: ctx, key: key, sc: stripesdk.NewClient(key), start: time.Now()}
	spec := freshSpec(t, "subscription-tool.json")
	c, err := stripe.New(spec, stripe.Config{SecretKey: key, AllowedURLHosts: testHosts})
	l.must(err, "new client")
	t.Logf("app %s", spec.App)
	t.Cleanup(func() { l.archive(spec) })

	monthly := payforge.PlanLookupKey(spec.App, "plus", pricingspec.IntervalMonth)
	setPlus := func(cents int64) {
		for i := range spec.Plans {
			for j := range spec.Plans[i].Prices {
				if spec.Plans[i].ID == "plus" && spec.Plans[i].Prices[j].Interval == pricingspec.IntervalMonth {
					spec.Plans[i].Prices[j].AmountCents = cents
				}
			}
		}
	}

	t.Run("sync creates the catalog, repeats as unchanged, and survives A to B to A", func(t *testing.T) {
		dry, err := c.Sync(ctx, true)
		l.must(err, "dry run")
		if !dry.Changed() || !dry.DryRun {
			t.Fatalf("a dry run on an empty account = %+v", dry)
		}
		if got := l.priceByKey(monthly); got != nil {
			t.Fatalf("a dry run created %s", got.ID)
		}
		_, err = c.Sync(ctx, false)
		l.must(err, "sync")
		again, err := c.Sync(ctx, false)
		l.must(err, "second sync")
		if again.Changed() {
			t.Fatalf("a second sync changed things: %+v", again.Actions)
		}
		for _, cents := range []int64{1500, 1200} {
			setPlus(cents)
			_, err := c.Sync(ctx, false)
			l.must(err, fmt.Sprintf("sync at %d", cents))
			if p := l.priceByKey(monthly); p == nil || p.UnitAmount != cents {
				t.Fatalf("after %d the key holds %+v", cents, p)
			}
		}
	})

	var customer string
	t.Run("a customer is created once, and a changed email does not fail", func(t *testing.T) {
		first, err := c.EnsureCustomer(ctx, payforge.Subject{ID: "org-live", Name: "Live", Email: "a@example.com"})
		l.must(err, "first")
		customer = first
		t.Cleanup(func() {
			if _, err := l.sc.V1Customers.Delete(ctx, first, nil); err != nil {
				t.Logf("cleanup: delete customer: %v", err)
			}
		})
		second, err := c.EnsureCustomer(ctx, payforge.Subject{ID: "org-live", Name: "Live", Email: "b@example.com"})
		l.must(err, "a changed email")
		l.observe("a second EnsureCustomer straight after the first returned the same customer: %v", first == second)
		if second != first {
			t.Cleanup(func() { _, _ = l.sc.V1Customers.Delete(ctx, second, nil) }) //nolint:errcheck // best-effort cleanup of a duplicate
		}
	})

	t.Run("a plan checkout is a hosted session carrying our metadata, and repeats", func(t *testing.T) {
		in := payforge.PlanCheckout{
			Subject: payforge.Subject{ID: "org-live", Name: "Live", Email: "a@example.com"}, Plan: "plus",
			Interval: pricingspec.IntervalMonth, SuccessURL: "https://app.example.com/billing#done", CancelURL: "https://app.example.com/pricing",
		}
		s1, err := c.CheckoutPlan(ctx, in)
		l.must(err, "checkout")
		if !strings.HasPrefix(s1.URL, "https://checkout.stripe.com/") {
			t.Fatalf("url = %q", s1.URL)
		}
		got, err := l.sc.V1CheckoutSessions.Retrieve(ctx, s1.ID, nil)
		l.must(err, "retrieve session")
		if got.Mode != stripesdk.CheckoutSessionModeSubscription || got.Metadata[payforge.MetaApp] != spec.App ||
			got.Metadata[payforge.MetaPlan] != "plus" || !strings.Contains(got.SuccessURL, "{CHECKOUT_SESSION_ID}") {
			t.Fatalf("session = mode %s, metadata %v, success %q", got.Mode, got.Metadata, got.SuccessURL)
		}
		s2, err := c.CheckoutPlan(ctx, in)
		l.must(err, "repeat")
		// The repeat expires the first, so only one can be paid.
		first, err := l.sc.V1CheckoutSessions.Retrieve(ctx, s1.ID, nil)
		l.must(err, "retrieve first session")
		if s2.ID == s1.ID || s2.URL == "" || first.Status != stripesdk.CheckoutSessionStatusExpired {
			t.Fatalf("repeat: first %s is %s, second %s", s1.ID, first.Status, s2.ID)
		}
	})

	t.Run("a real subscription is read, blocks a second checkout, and its events drive the lifecycle", func(t *testing.T) {
		pm, err := l.sc.V1PaymentMethods.Attach(ctx, "pm_card_visa", &stripesdk.PaymentMethodAttachParams{Customer: stripesdk.String(customer)})
		l.must(err, "attach test card")
		price := l.priceByKey(monthly)
		if price == nil {
			t.Fatal("no price")
		}
		sub, err := l.sc.V1Subscriptions.Create(ctx, &stripesdk.SubscriptionCreateParams{
			Customer: stripesdk.String(customer), DefaultPaymentMethod: stripesdk.String(pm.ID),
			Items: []*stripesdk.SubscriptionCreateItemParams{{Price: stripesdk.String(price.ID)}},
		})
		l.must(err, "create subscription")
		t.Cleanup(func() {
			if _, err := l.sc.V1Subscriptions.Cancel(ctx, sub.ID, nil); err != nil {
				t.Logf("cleanup: cancel subscription: %v", err)
			}
		})

		subs, err := c.Subscriptions(ctx, customer)
		l.must(err, "Subscriptions")
		if len(subs) != 1 || !payforge.Grants(subs[0].Status) || subs[0].PeriodEnd.IsZero() ||
			len(subs[0].Items) != 1 || subs[0].Items[0].LookupKey != monthly {
			t.Fatalf("subscriptions = %+v", subs)
		}
		in := payforge.PlanCheckout{Subject: payforge.Subject{ID: "org-live"}, Plan: "plus", Interval: pricingspec.IntervalMonth,
			SuccessURL: "https://app.example.com/ok", CancelURL: "https://app.example.com/no"}
		if _, err := c.CheckoutPlan(ctx, in); !errors.Is(err, payforge.ErrAlreadySubscribed) {
			t.Fatalf("a second checkout: %v", err)
		}

		// Real events, signed here with a secret of our own, through the real reader.
		store, sink := payforge.NewMemoryStore(), payforge.NewMemorySink()
		store.Link(customer, "org-live")
		life := &payforge.Lifecycle{Spec: spec, Store: store, Sink: sink, Provider: c}
		raw := l.waitEvent("customer.subscription.created", customer)
		header, wh := signed(raw)
		ev, err := wh.Parse(raw, header)
		l.must(err, "parse the created event")
		if ev.Kind != payforge.EventSubscriptionCreated || ev.Subscription == nil || ev.Subscription.PeriodEnd.IsZero() ||
			len(ev.Subscription.Items) == 0 || ev.Subscription.Items[0].LookupKey != monthly {
			t.Fatalf("event = %+v, subscription = %+v", ev, ev.Subscription)
		}
		res, err := life.Handle(ctx, ev)
		l.must(err, "handle created")
		if res.Outcome != payforge.OutcomeApplied || res.Entitlement.Plan != "plus" || !res.Entitlement.Granted {
			t.Fatalf("created: %+v", res)
		}

		_, err = l.sc.V1Subscriptions.Cancel(ctx, sub.ID, nil)
		l.must(err, "cancel")
		raw = l.waitEvent("customer.subscription.deleted", customer)
		header, wh = signed(raw)
		ev, err = wh.Parse(raw, header)
		l.must(err, "parse the deleted event")
		if ev.Kind != payforge.EventSubscriptionEnded {
			t.Fatalf("kind = %s", ev.Kind)
		}
		res, err = life.Handle(ctx, ev)
		l.must(err, "handle deleted")
		if res.Entitlement.Granted || res.Entitlement.Plan != "free" {
			t.Fatalf("deleted: %+v", res.Entitlement)
		}
	})
}

func TestLiveConnect(t *testing.T) {
	key := liveKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	l := &live{t: t, ctx: ctx, key: key, sc: stripesdk.NewClient(key), start: time.Now()}
	spec := freshSpec(t, "marketplace.json")
	c, err := stripe.New(spec, stripe.Config{SecretKey: key, AllowedURLHosts: testHosts})
	l.must(err, "new client")
	t.Cleanup(func() { l.archive(spec) })
	_, err = c.Sync(ctx, false)
	l.must(err, "sync")

	seller := payforge.Subject{ID: "seller-live", Email: "seller@example.com"}
	acct, err := c.CreateAccount(ctx, seller, "US")
	if err != nil && strings.Contains(err.Error(), "Connect") {
		t.Skipf("this Stripe account has not enabled Connect: %v", err)
	}
	l.must(err, "create account")
	t.Cleanup(func() {
		if _, err := l.sc.V1Accounts.Delete(ctx, acct, nil); err != nil {
			t.Logf("cleanup: delete account %s: %v", acct, err)
		}
	})

	if _, err := c.CreateAccount(ctx, payforge.Subject{ID: seller.ID, Email: "other@example.com"}, "US"); err != nil {
		t.Fatalf("a changed email on the same seller: %v", err)
	}
	state, err := c.Account(ctx, acct)
	l.must(err, "read account")
	if state.ChargesEnabled {
		t.Fatal("a new account can already take charges")
	}
	link, err := c.OnboardingLink(ctx, acct, "https://app.example.com/refresh", "https://app.example.com/return")
	l.must(err, "onboarding link")
	if !strings.HasPrefix(link, "https://connect.stripe.com/") {
		t.Fatalf("link = %q", link)
	}
	_, err = c.CheckoutSale(ctx, payforge.Sale{
		Buyer: payforge.Subject{ID: "buyer-live"}, SellerAccount: acct, SellerPlan: "seller", Product: "listing",
		AmountCents: 2500, Reference: "live-1", SuccessURL: "https://app.example.com/ok", CancelURL: "https://app.example.com/no",
	})
	if !errors.Is(err, payforge.ErrSellerNotReady) {
		t.Fatalf("a sale by a seller who has not onboarded: %v", err)
	}
	l.observe("a new Express account in the US requests card_payments and transfers without error; charges_enabled=%v", state.ChargesEnabled)
}
