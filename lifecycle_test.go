package payforge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/plexusone/payforge"
)

// fakeReader returns the subscriptions the provider "holds" for a customer.
type fakeReader struct {
	subs  []payforge.SubscriptionState
	err   error
	calls int
}

func (f *fakeReader) Subscriptions(context.Context, string) ([]payforge.SubscriptionState, error) {
	f.calls++
	return f.subs, f.err
}

// failingSink fails until fixed.
type failingSink struct {
	*payforge.MemorySink
	fail bool
}

func (f *failingSink) Apply(ctx context.Context, e payforge.Entitlement) error {
	if f.fail {
		return errors.New("sink is down")
	}
	return f.MemorySink.Apply(ctx, e)
}

var periodEnd = time.Unix(1795000000, 0).UTC()

func sub(id, status string, keys ...string) payforge.SubscriptionState {
	s := payforge.SubscriptionState{ID: id, Status: status, PeriodEnd: periodEnd}
	for i, k := range keys {
		s.Items = append(s.Items, payforge.SubscriptionItem{PriceID: "price_" + string(rune('a'+i)), LookupKey: k})
	}
	return s
}

func subEvent(id string, kind payforge.EventKind, s payforge.SubscriptionState) payforge.Event {
	return payforge.Event{
		ID: id, Type: "customer.subscription.x", Kind: kind, App: "taskboard", Subject: "org-1",
		CustomerID: "cus_1", Subscription: &s,
	}
}

type rig struct {
	l      *payforge.Lifecycle
	store  *payforge.MemoryStore
	sink   *failingSink
	reader *fakeReader
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: payforge.NewMemoryStore(), sink: &failingSink{MemorySink: payforge.NewMemorySink()}, reader: &fakeReader{}}
	r.l = &payforge.Lifecycle{Spec: load(t, "subscription-tool.json"), Store: r.store, Sink: r.sink, Provider: r.reader}
	return r
}

func (r *rig) handle(t *testing.T, ev payforge.Event) payforge.Result {
	t.Helper()
	res, err := r.l.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLifecycleGrantsWhileTheSubscriptionDoes(t *testing.T) {
	for status, granted := range map[string]bool{
		"active": true, "trialing": true, "past_due": true,
		"unpaid": false, "canceled": false, "incomplete": false, "incomplete_expired": false, "paused": false,
	} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t)
			s := sub("sub_1", status, "taskboard/plus/month")
			r.reader.subs = []payforge.SubscriptionState{s}
			res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, s))
			if res.Outcome != payforge.OutcomeApplied || res.Entitlement == nil {
				t.Fatalf("result = %+v", res)
			}
			e := *res.Entitlement
			if e.Granted != granted || e.SubscribedPlan != "plus" || e.Status != status || e.Subject != "org-1" {
				t.Fatalf("entitlement = %+v", e)
			}
			want := "free"
			if granted {
				want = "plus"
			}
			if e.Plan != want {
				t.Fatalf("plan = %q, want %q", e.Plan, want)
			}
			if got, ok := r.sink.Get("org-1"); !ok || got != e {
				t.Fatalf("sink holds %+v", got)
			}
			if e.Origin != payforge.OriginSubscription || e.EventID != "evt_1" || e.SubscriptionID != "sub_1" || e.CustomerID != "cus_1" {
				t.Fatalf("provenance = %+v", e)
			}
			if granted && !e.ValidUntil.Equal(periodEnd) {
				t.Fatalf("valid until = %v", e.ValidUntil)
			}
		})
	}
}

func TestLifecycleReplayChangesNothing(t *testing.T) {
	r := newRig(t)
	s := sub("sub_1", "active", "taskboard/plus/month")
	r.reader.subs = []payforge.SubscriptionState{s}
	ev := subEvent("evt_1", payforge.EventSubscriptionCreated, s)
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeApplied {
		t.Fatalf("first: %+v", res)
	}
	for i := 0; i < 3; i++ {
		if res := r.handle(t, ev); res.Outcome != payforge.OutcomeDuplicate {
			t.Fatalf("replay %d: %+v", i, res)
		}
	}
	if n := len(r.sink.History()); n != 1 {
		t.Fatalf("the sink was applied %d times", n)
	}
}

func TestLifecycleRetriesAFailedApply(t *testing.T) {
	r := newRig(t)
	s := sub("sub_1", "active", "taskboard/plus/month")
	r.reader.subs = []payforge.SubscriptionState{s}
	ev := subEvent("evt_1", payforge.EventSubscriptionCreated, s)

	r.sink.fail = true
	if _, err := r.l.Handle(context.Background(), ev); err == nil || !strings.Contains(err.Error(), "sink is down") {
		t.Fatalf("err = %v", err)
	}
	if seen, _ := r.store.Seen(context.Background(), "evt_1"); seen {
		t.Fatal("a failed event was recorded as seen, so the provider's redelivery would be dropped")
	}
	r.sink.fail = false
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeApplied {
		t.Fatalf("retry: %+v", res)
	}
}

// The defect the library exists to fix: a late event must not undo a newer state.
func TestLifecycleLateEventsCannotUndoAnythingWithTheProviderRead(t *testing.T) {
	t.Run("a late update to active after the subscription ended", func(t *testing.T) {
		r := newRig(t)
		r.reader.subs = nil // the provider holds no live subscription
		stale := sub("sub_1", "active", "taskboard/plus/month")
		res := r.handle(t, subEvent("evt_late", payforge.EventSubscriptionUpdated, stale))
		if e := res.Entitlement; e == nil || e.Granted || e.Plan != "free" {
			t.Fatalf("entitlement = %+v", e)
		}
	})
	t.Run("a late end of an old subscription while a new one is live", func(t *testing.T) {
		r := newRig(t)
		r.reader.subs = []payforge.SubscriptionState{sub("sub_2", "active", "taskboard/business/month")}
		old := sub("sub_1", "canceled", "taskboard/plus/month")
		res := r.handle(t, subEvent("evt_late", payforge.EventSubscriptionEnded, old))
		if e := res.Entitlement; e == nil || !e.Granted || e.Plan != "business" || e.SubscriptionID != "sub_2" {
			t.Fatalf("entitlement = %+v", e)
		}
	})
	t.Run("the same events in any order end in the same place", func(t *testing.T) {
		live := sub("sub_2", "active", "taskboard/business/month")
		events := []payforge.Event{
			subEvent("e1", payforge.EventSubscriptionCreated, sub("sub_1", "active", "taskboard/plus/month")),
			subEvent("e2", payforge.EventSubscriptionEnded, sub("sub_1", "canceled", "taskboard/plus/month")),
			subEvent("e3", payforge.EventSubscriptionCreated, live),
		}
		orders := [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}, {1, 2, 0}, {0, 2, 1}, {2, 0, 1}}
		for _, order := range orders {
			r := newRig(t)
			r.reader.subs = []payforge.SubscriptionState{live}
			for _, i := range order {
				r.handle(t, events[i])
			}
			if e, _ := r.sink.Get("org-1"); e.Plan != "business" || !e.Granted {
				t.Errorf("order %v ended on %+v", order, e)
			}
		}
	})
}

func TestLifecycleWithoutTheProviderTheEventDecides(t *testing.T) {
	r := newRig(t)
	r.l.Provider = nil
	res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, sub("sub_1", "active", "taskboard/plus/month")))
	if e := res.Entitlement; e == nil || e.Plan != "plus" || !e.Granted {
		t.Fatalf("entitlement = %+v", e)
	}
	// An ended subscription must not grant whatever status the event carried.
	res = r.handle(t, subEvent("evt_2", payforge.EventSubscriptionEnded, sub("sub_1", "active", "taskboard/plus/month")))
	if e := res.Entitlement; e == nil || e.Granted || e.Plan != "free" {
		t.Fatalf("ended: %+v", e)
	}
}

func TestLifecycleAPriceTheSpecDoesNotKnowNeverGrants(t *testing.T) {
	for name, keys := range map[string][]string{
		"a plan the spec lacks":   {"taskboard/gone/month"},
		"another app's price":     {"craftmarket/seller/month"},
		"a price with no key":     {""},
		"a key that is not ours":  {"something-else"},
		"an arranged plan's name": {"taskboard/enterprise/month"},
		"an interval it lacks":    {"taskboard/plus/week"},
		"a free plan's name":      {"taskboard/free/month"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			s := sub("sub_1", "active", keys...)
			r.reader.subs = []payforge.SubscriptionState{s}
			res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, s))
			e := res.Entitlement
			if e == nil || e.Granted || e.Plan != "free" || e.SubscribedPlan == "plus" {
				t.Fatalf("entitlement = %+v", e)
			}
		})
	}
}

func TestLifecycleTheHighestPlanWinsAcrossLinesAndSubscriptions(t *testing.T) {
	r := newRig(t)
	r.reader.subs = []payforge.SubscriptionState{
		sub("sub_1", "active", "taskboard/plus/month"),
		sub("sub_2", "active", "taskboard/plus/year", "taskboard/business/month"), // two lines
		sub("sub_3", "canceled", "taskboard/business/year"),                       // higher, but not granting
	}
	res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, r.reader.subs[0]))
	if e := res.Entitlement; e == nil || e.Plan != "business" || e.SubscriptionID != "sub_2" {
		t.Fatalf("entitlement = %+v", e)
	}
}

func TestLifecycleTheLaterPeriodBreaksATie(t *testing.T) {
	r := newRig(t)
	a, b := sub("sub_a", "active", "taskboard/plus/month"), sub("sub_b", "active", "taskboard/plus/year")
	b.PeriodEnd = periodEnd.Add(24 * time.Hour)
	r.reader.subs = []payforge.SubscriptionState{a, b}
	res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, a))
	if e := res.Entitlement; e == nil || e.SubscriptionID != "sub_b" {
		t.Fatalf("entitlement = %+v", e)
	}
}

func TestLifecycleLegacySubscriptionsNameTheirPlanInMetadata(t *testing.T) {
	r := newRig(t)
	r.l.LegacyPrefixes = []string{"myapp"}
	s := sub("sub_1", "active", "") // a price from before payforge: no lookup key
	s.Metadata = map[string]string{"myapp_plan": "plus"}
	r.reader.subs = []payforge.SubscriptionState{s}
	res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, s))
	if e := res.Entitlement; e == nil || e.Plan != "plus" || !e.Granted {
		t.Fatalf("entitlement = %+v", e)
	}
	// Without the prefix configured the metadata means nothing.
	r2 := newRig(t)
	r2.reader.subs = []payforge.SubscriptionState{s}
	if e := r2.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, s)).Entitlement; e == nil || e.Granted {
		t.Fatalf("unconfigured: %+v", e)
	}
}

func TestLifecycleFindsTheAccountThroughTheStore(t *testing.T) {
	r := newRig(t)
	s := sub("sub_1", "active", "taskboard/plus/month")
	r.reader.subs = []payforge.SubscriptionState{s}
	ev := subEvent("evt_1", payforge.EventSubscriptionUpdated, s)
	ev.Subject = ""

	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped || !strings.Contains(res.Reason, "no account") {
		t.Fatalf("unknown customer: %+v", res)
	}
	if n := len(r.sink.History()); n != 0 {
		t.Fatal("the sink was applied for an unknown account")
	}
	if seen, _ := r.store.Seen(context.Background(), "evt_1"); seen {
		t.Fatal("an event for an unknown account was recorded, so its redelivery could never apply")
	}
	r.store.Link("cus_1", "org-9")
	// The provider redelivers the very same event: it was not recorded, so it applies.
	res := r.handle(t, ev)
	if res.Outcome != payforge.OutcomeApplied || res.Entitlement.Subject != "org-9" {
		t.Fatalf("linked customer: %+v", res)
	}
}

func TestLifecycleLeavesOtherProductsAlone(t *testing.T) {
	r := newRig(t)
	ev := subEvent("evt_1", payforge.EventSubscriptionUpdated, sub("sub_1", "active", "craftmarket/seller/month"))
	ev.App = "craftmarket"
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped || len(r.sink.History()) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if r.reader.calls != 0 {
		t.Fatal("the provider was read for another product's event")
	}
}

func TestLifecycleIgnoresEventsItDoesNotActOn(t *testing.T) {
	r := newRig(t)
	for _, k := range []payforge.EventKind{
		payforge.EventCheckoutCompleted, payforge.EventInvoicePaid, payforge.EventInvoiceFailed,
		payforge.EventAccountUpdated, payforge.EventRefunded, payforge.EventIgnored,
	} {
		res := r.handle(t, payforge.Event{ID: "evt_" + string(k), Kind: k})
		if res.Outcome != payforge.OutcomeIgnored || res.Reason == "" {
			t.Errorf("%s: %+v", k, res)
		}
		if seen, _ := r.store.Seen(context.Background(), "evt_"+string(k)); seen {
			t.Errorf("%s: an ignored event was stored", k)
		}
	}
	if len(r.sink.History()) != 0 || r.reader.calls != 0 {
		t.Fatal("an ignored event did work")
	}
}

type brokenStore struct {
	*payforge.MemoryStore
	seenErr, markErr, subjectErr error
}

func (b brokenStore) Seen(ctx context.Context, id string) (bool, error) {
	if b.seenErr != nil {
		return false, b.seenErr
	}
	return b.MemoryStore.Seen(ctx, id)
}

func (b brokenStore) MarkSeen(ctx context.Context, id string) error {
	if b.markErr != nil {
		return b.markErr
	}
	return b.MemoryStore.MarkSeen(ctx, id)
}

func (b brokenStore) SubjectForCustomer(ctx context.Context, id string) (string, error) {
	if b.subjectErr != nil {
		return "", b.subjectErr
	}
	return b.MemoryStore.SubjectForCustomer(ctx, id)
}

func TestLifecycleReportsFailuresInsteadOfSwallowingThem(t *testing.T) {
	boom := errors.New("boom")
	s := sub("sub_1", "active", "taskboard/plus/month")
	for name, tc := range map[string]struct {
		store  brokenStore
		reader error
		mut    func(*payforge.Event)
		want   string
	}{
		"seen":    {brokenStore{MemoryStore: payforge.NewMemoryStore(), seenErr: boom}, nil, nil, "check event"},
		"mark":    {brokenStore{MemoryStore: payforge.NewMemoryStore(), markErr: boom}, nil, nil, "record event"},
		"subject": {brokenStore{MemoryStore: payforge.NewMemoryStore(), subjectErr: boom}, nil, func(e *payforge.Event) { e.Subject = "" }, "find account"},
		"reader":  {brokenStore{MemoryStore: payforge.NewMemoryStore()}, boom, nil, "read subscriptions"},
	} {
		t.Run(name, func(t *testing.T) {
			sink := payforge.NewMemorySink()
			l := &payforge.Lifecycle{Spec: load(t, "subscription-tool.json"), Store: tc.store, Sink: sink,
				Provider: &fakeReader{subs: []payforge.SubscriptionState{s}, err: tc.reader}}
			ev := subEvent("evt_1", payforge.EventSubscriptionUpdated, s)
			if tc.mut != nil {
				tc.mut(&ev)
			}
			_, err := l.Handle(context.Background(), ev)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if name == "reader" || name == "seen" {
				if len(sink.History()) != 0 {
					t.Fatal("the sink was applied despite the failure")
				}
			}
		})
	}
	t.Run("skipping an unknown account fails if it cannot be recorded", func(t *testing.T) {
		store := brokenStore{MemoryStore: payforge.NewMemoryStore(), markErr: boom}
		l := &payforge.Lifecycle{Spec: load(t, "subscription-tool.json"), Store: store, Sink: payforge.NewMemorySink()}
		ev := subEvent("evt_1", payforge.EventSubscriptionUpdated, s)
		ev.App = "other"
		if _, err := l.Handle(context.Background(), ev); err == nil {
			t.Fatal("no error")
		}
	})
}

func TestLifecycleNeedsItsParts(t *testing.T) {
	spec := load(t, "subscription-tool.json")
	for name, l := range map[string]*payforge.Lifecycle{
		"no spec":  {Store: payforge.NewMemoryStore(), Sink: payforge.NewMemorySink()},
		"no store": {Spec: spec, Sink: payforge.NewMemorySink()},
		"no sink":  {Spec: spec, Store: payforge.NewMemoryStore()},
	} {
		if _, err := l.Handle(context.Background(), payforge.Event{Kind: payforge.EventSubscriptionUpdated}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMetadataHelpers(t *testing.T) {
	meta := map[string]string{"payforge_plan": "a", "old_plan": "b", "old_organization_id": "o1", "other_organization_id": "o2"}
	if got := payforge.MetadataPlan(meta, []string{"old"}); got != "a" {
		t.Errorf("plan = %q, payforge's key should win", got)
	}
	if got := payforge.MetadataPlan(map[string]string{"old_plan": "b"}, []string{"x", "old"}); got != "b" {
		t.Errorf("legacy plan = %q", got)
	}
	if got := payforge.MetadataSubject(meta, []string{"other", "old"}); got != "o2" {
		t.Errorf("subject = %q, the first prefix that has one should win", got)
	}
	if got := payforge.MetadataSubject(meta, nil); got != "" {
		t.Errorf("subject without prefixes = %q", got)
	}
}

func TestLifecycleRefusesAnEventWithNoID(t *testing.T) {
	r := newRig(t)
	ev := subEvent("", payforge.EventSubscriptionUpdated, sub("sub_1", "active", "taskboard/plus/month"))
	if _, err := r.l.Handle(context.Background(), ev); err == nil || !strings.Contains(err.Error(), "no ID") {
		t.Fatalf("err = %v", err)
	}
	// Nothing was recorded, so a second event with no ID is not a "duplicate" of the first.
	if seen, _ := r.store.Seen(context.Background(), ""); seen {
		t.Fatal("the empty ID was recorded")
	}
}

func TestLifecycleSkipsAndRemembersAMalformedEvent(t *testing.T) {
	r := newRig(t)
	ev := subEvent("evt_1", payforge.EventSubscriptionUpdated, sub("sub_1", "active"))
	ev.Subscription = nil
	res := r.handle(t, ev) // no error: a retry cannot fix it, so Stripe must not redeliver for days
	if res.Outcome != payforge.OutcomeSkipped || res.Reason == "" {
		t.Fatalf("res = %+v", res)
	}
	if seen, _ := r.store.Seen(context.Background(), "evt_1"); !seen {
		t.Fatal("a malformed event was not remembered")
	}
}

func TestLifecycleNeverActsOnAnEventFromAConnectedAccount(t *testing.T) {
	// A seller controls the objects in their own Stripe account, metadata and prices
	// included. Their events arrive on the shared handler and must not change a plan.
	r := newRig(t)
	forged := subEvent("evt_1", payforge.EventSubscriptionCreated, sub("sub_x", "active", "taskboard/business/month"))
	forged.Account = "acct_seller123"
	res := r.handle(t, forged)
	if res.Outcome != payforge.OutcomeSkipped || !strings.Contains(res.Reason, "connected account") {
		t.Fatalf("res = %+v", res)
	}
	if len(r.sink.History()) != 0 || r.reader.calls != 0 {
		t.Fatal("a connected account's event did work")
	}
}

func TestLifecycleAPriceWithAKeyThatIsNotOursNeverFallsBackToMetadata(t *testing.T) {
	r := newRig(t)
	s := sub("sub_1", "active", "pro_monthly") // made by hand, with a key of its own
	s.Metadata = map[string]string{payforge.MetaPlan: "plus"}
	r.reader.subs = []payforge.SubscriptionState{s}
	res := r.handle(t, subEvent("evt_1", payforge.EventSubscriptionUpdated, s))
	if res.Entitlement.Granted || res.Entitlement.Plan != "free" {
		t.Fatalf("entitlement = %+v", res.Entitlement)
	}
}

func TestLifecycleTheSameTierResolvesByPlanIDWhateverTheLineOrder(t *testing.T) {
	spec := load(t, "marketplace.json")
	// Two plans sharing a tier: the line order must not decide.
	for i := range spec.Plans {
		if spec.Plans[i].ID == "pro_seller" {
			spec.Plans[i].Tier = "seller"
		}
	}
	for _, keys := range [][]string{
		{"craftmarket/seller/month", "craftmarket/pro_seller/month"},
		{"craftmarket/pro_seller/month", "craftmarket/seller/month"},
	} {
		l := &payforge.Lifecycle{Spec: spec, Store: payforge.NewMemoryStore(), Sink: payforge.NewMemorySink()}
		ev := subEvent("evt_x", payforge.EventSubscriptionCreated, sub("sub_1", "active", keys...))
		ev.App = "craftmarket"
		res, err := l.Handle(context.Background(), ev)
		if err != nil || res.Entitlement.Plan != "pro_seller" {
			t.Fatalf("%v: plan = %+v, err = %v", keys, res.Entitlement, err)
		}
	}
}
