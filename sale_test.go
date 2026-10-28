package payforge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/plexusone/payforge"
)

// fakeAccounts returns the state the provider "holds" for a connected account.
type fakeAccounts struct {
	state payforge.AccountState
	err   error
	calls int
}

func (f *fakeAccounts) Account(context.Context, string) (payforge.AccountState, error) {
	f.calls++
	return f.state, f.err
}

type saleRig struct {
	l        *payforge.Lifecycle
	store    *payforge.MemoryStore
	sink     *payforge.MemorySink
	accounts *fakeAccounts
}

func newSaleRig(t *testing.T) *saleRig {
	t.Helper()
	r := &saleRig{store: payforge.NewMemoryStore(), sink: payforge.NewMemorySink(), accounts: &fakeAccounts{}}
	r.l = &payforge.Lifecycle{
		Spec: load(t, "marketplace.json"), Store: r.store, Sink: r.sink,
		Sales: r.sink, Sellers: r.sink, Accounts: r.accounts,
	}
	return r
}

func (r *saleRig) handle(t *testing.T, ev payforge.Event) payforge.Result {
	t.Helper()
	res, err := r.l.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func refundEvent(id string, refunded int64, full bool) payforge.Event {
	return payforge.Event{
		ID: id, Type: "charge.refunded", Kind: payforge.EventRefunded, App: "craftmarket",
		Subject: "buyer-1", Product: "listing", CustomerID: "cus_buyer",
		Metadata: map[string]string{payforge.MetaSeller: "acct_seller123"},
		Refund: &payforge.RefundResult{
			ChargeID: "ch_1", PaymentIntent: "pi_1", AmountCents: 2000, RefundedCents: refunded, Full: full,
		},
	}
}

func accountEvent(id, app, subject string) payforge.Event {
	return payforge.Event{
		ID: id, Type: "account.updated", Kind: payforge.EventAccountUpdated, App: app, Subject: subject,
		Account:      "acct_seller123",
		AccountState: &payforge.AccountState{ID: "acct_seller123", ChargesEnabled: true, DetailsSubmitted: true},
	}
}

func TestRefundIsAppliedWithWhatWasBought(t *testing.T) {
	r := newSaleRig(t)
	res := r.handle(t, refundEvent("evt_1", 500, false))
	if res.Outcome != payforge.OutcomeApplied || res.Refund == nil {
		t.Fatalf("res = %+v", res)
	}
	got, ok := r.sink.Refund("ch_1")
	want := payforge.SaleRefund{
		Subject: "buyer-1", Seller: "acct_seller123", Product: "listing", ChargeID: "ch_1", PaymentIntent: "pi_1",
		AmountCents: 2000, RefundedCents: 500, EventID: "evt_1",
	}
	if !ok || got != want {
		t.Fatalf("held %+v, want %+v", got, want)
	}
	if again := r.handle(t, refundEvent("evt_1", 500, false)); again.Outcome != payforge.OutcomeDuplicate {
		t.Fatalf("replay = %+v", again)
	}
}

func TestAFullRefundStaysFullWhateverOrderEventsArriveIn(t *testing.T) {
	r := newSaleRig(t)
	r.handle(t, refundEvent("evt_full", 2000, true))
	// A partial refund's event delivered late must not reopen the sale.
	r.handle(t, refundEvent("evt_late", 500, false))
	got, _ := r.sink.Refund("ch_1")
	if !got.Full || got.RefundedCents != 2000 {
		t.Fatalf("held %+v", got)
	}
}

func TestRefundsOfAnythingButASaleAreSkippedAndRemembered(t *testing.T) {
	r := newSaleRig(t)
	for name, mod := range map[string]func(*payforge.Event){
		"a subscription's invoice": func(e *payforge.Event) { e.Product = "" },
		"another app":              func(e *payforge.Event) { e.App = "other" },
		"connected account":        func(e *payforge.Event) { e.Account = "acct_seller123" },
	} {
		ev := refundEvent("evt_"+name, 100, false)
		mod(&ev)
		res := r.handle(t, ev)
		if res.Outcome != payforge.OutcomeSkipped || res.Reason == "" {
			t.Errorf("%s: %+v", name, res)
		}
		if seen, _ := r.store.Seen(context.Background(), ev.ID); !seen {
			t.Errorf("%s: a skipped event was not remembered", name)
		}
	}
	if _, ok := r.sink.Refund("ch_1"); ok {
		t.Fatal("a skipped refund reached the sink")
	}
}

func TestRefundFindsTheBuyerThroughTheStore(t *testing.T) {
	r := newSaleRig(t)
	r.store.Link("cus_buyer", "buyer-9")
	ev := refundEvent("evt_1", 100, false)
	ev.Subject = ""
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeApplied || res.Refund.Subject != "buyer-9" {
		t.Fatalf("res = %+v", res)
	}
}

func TestSellerAccountAppliesTheProvidersCurrentState(t *testing.T) {
	r := newSaleRig(t)
	// The event says charges are on; the provider says they have since gone off.
	r.accounts.state = payforge.AccountState{ID: "acct_seller123", ChargesEnabled: false, DetailsSubmitted: true}
	res := r.handle(t, accountEvent("evt_1", "craftmarket", "seller-1"))
	if res.Outcome != payforge.OutcomeApplied || res.Seller == nil {
		t.Fatalf("res = %+v", res)
	}
	got, _ := r.sink.Seller("acct_seller123")
	if got.ChargesEnabled || !got.DetailsSubmitted || got.Subject != "seller-1" || got.EventID != "evt_1" {
		t.Fatalf("held %+v", got)
	}
	if r.accounts.calls != 1 {
		t.Errorf("provider read %d times", r.accounts.calls)
	}
}

func TestSellerAccountWithoutAReaderTrustsTheEvent(t *testing.T) {
	r := newSaleRig(t)
	r.l.Accounts = nil
	r.handle(t, accountEvent("evt_1", "craftmarket", "seller-1"))
	if got, _ := r.sink.Seller("acct_seller123"); !got.ChargesEnabled {
		t.Fatalf("held %+v", got)
	}
}

func TestAccountsNotCreatedHereAreSkipped(t *testing.T) {
	r := newSaleRig(t)
	for name, ev := range map[string]payforge.Event{
		"no metadata": accountEvent("evt_a", "", ""),
		"another app": accountEvent("evt_b", "other", "seller-1"),
	} {
		if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if r.accounts.calls != 0 {
		t.Error("the provider was read for an account that is not ours")
	}
}

func TestSaleEventsAreIgnoredWithoutTheirSinks(t *testing.T) {
	r := newSaleRig(t)
	r.l.Sales, r.l.Sellers = nil, nil
	for _, ev := range []payforge.Event{refundEvent("evt_r", 1, false), accountEvent("evt_a", "craftmarket", "s")} {
		res := r.handle(t, ev)
		if res.Outcome != payforge.OutcomeIgnored || res.Reason == "" {
			t.Errorf("%s: %+v", ev.Type, res)
		}
		if seen, _ := r.store.Seen(context.Background(), ev.ID); seen {
			t.Errorf("%s: an ignored event was stored", ev.Type)
		}
	}
}

type failingSale struct {
	*payforge.MemorySink
	fail bool
}

func (f *failingSale) ApplyRefund(ctx context.Context, r payforge.SaleRefund) error {
	if f.fail {
		return errors.New("sale sink is down")
	}
	return f.MemorySink.ApplyRefund(ctx, r)
}

func (f *failingSale) ApplySeller(ctx context.Context, a payforge.SellerAccount) error {
	if f.fail {
		return errors.New("seller sink is down")
	}
	return f.MemorySink.ApplySeller(ctx, a)
}

func TestSaleEventsRetryAfterAFailureAndReportIt(t *testing.T) {
	r := newSaleRig(t)
	bad := &failingSale{MemorySink: r.sink, fail: true}
	r.l.Sales, r.l.Sellers = bad, bad
	ctx := context.Background()
	for _, ev := range []payforge.Event{refundEvent("evt_r", 100, false), accountEvent("evt_a", "craftmarket", "s")} {
		if _, err := r.l.Handle(ctx, ev); err == nil {
			t.Fatalf("%s: the failure was swallowed", ev.Type)
		}
		if seen, _ := r.store.Seen(ctx, ev.ID); seen {
			t.Fatalf("%s: a failed event was recorded", ev.Type)
		}
	}
	bad.fail = false
	for _, ev := range []payforge.Event{refundEvent("evt_r", 100, false), accountEvent("evt_a", "craftmarket", "s")} {
		if res := r.handle(t, ev); res.Outcome != payforge.OutcomeApplied {
			t.Errorf("%s retry: %+v", ev.Type, res)
		}
	}

	r.accounts.err = errors.New("stripe is down")
	if _, err := r.l.Handle(ctx, accountEvent("evt_b", "craftmarket", "s")); err == nil {
		t.Error("a provider failure was swallowed")
	}
	// An event with no ID cannot be processed once.
	for _, k := range []payforge.EventKind{payforge.EventRefunded, payforge.EventAccountUpdated} {
		if _, err := r.l.Handle(ctx, payforge.Event{Kind: k}); err == nil {
			t.Errorf("%s: an event with no ID was accepted", k)
		}
	}
}

func TestSaleEventsReportStoreFailures(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]brokenStore{
		"check":   {MemoryStore: payforge.NewMemoryStore(), seenErr: errors.New("down")},
		"record":  {MemoryStore: payforge.NewMemoryStore(), markErr: errors.New("down")},
		"account": {MemoryStore: payforge.NewMemoryStore(), subjectErr: errors.New("down")},
	} {
		r := newSaleRig(t)
		r.l.Store = store
		ev := refundEvent("evt_r", 100, false)
		if name == "account" {
			ev.Subject = ""
		}
		if _, err := r.l.Handle(ctx, ev); err == nil {
			t.Errorf("refund, %s: the failure was swallowed", name)
		}
		if _, err := r.l.Handle(ctx, accountEvent("evt_a", "craftmarket", "s")); name != "account" && err == nil {
			t.Errorf("account, %s: the failure was swallowed", name)
		}
		// A skipped event must also report a store that cannot record it.
		if name == "record" {
			skipped := refundEvent("evt_s", 100, false)
			skipped.Product = ""
			if _, err := r.l.Handle(ctx, skipped); err == nil {
				t.Error("a skip that could not be recorded was swallowed")
			}
		}
	}
}

func TestAMalformedSaleEventIsSkippedAndRemembered(t *testing.T) {
	r := newSaleRig(t)
	for _, k := range []payforge.EventKind{payforge.EventRefunded, payforge.EventAccountUpdated} {
		id := "evt_" + string(k)
		res := r.handle(t, payforge.Event{ID: id, Kind: k})
		if res.Outcome != payforge.OutcomeSkipped || res.Reason == "" {
			t.Errorf("%s: %+v", k, res)
		}
		if seen, _ := r.store.Seen(context.Background(), id); !seen {
			t.Errorf("%s: not remembered", k)
		}
	}
}

func TestARefundForAnUnknownBuyerAppliesWhenRedeliveredAfterTheLink(t *testing.T) {
	r := newSaleRig(t)
	ev := refundEvent("evt_1", 100, false)
	ev.Subject, ev.CustomerID = "", "cus_unknown"
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped {
		t.Fatalf("res = %+v", res)
	}
	if seen, _ := r.store.Seen(context.Background(), "evt_1"); seen {
		t.Fatal("recorded, so the redelivery could never apply")
	}
	r.store.Link("cus_unknown", "buyer-7")
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeApplied || res.Refund.Subject != "buyer-7" {
		t.Fatalf("redelivery = %+v", res)
	}
}

func TestARefundFromAConnectedAccountIsNeverApplied(t *testing.T) {
	r := newSaleRig(t)
	forged := refundEvent("evt_1", 2000, true)
	forged.Account = "acct_seller123"
	if res := r.handle(t, forged); res.Outcome != payforge.OutcomeSkipped {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := r.sink.Refund("ch_1"); ok {
		t.Fatal("a seller's forged refund reached the sink")
	}
}

func disputeEvent(id string, kind payforge.EventKind, status string) payforge.Event {
	return payforge.Event{
		ID: id, Type: "charge.dispute.x", Kind: kind,
		Dispute: &payforge.DisputeResult{ID: "dp_1", ChargeID: "ch_1", PaymentIntent: "pi_1",
			AmountCents: 2000, Currency: "usd", Reason: "fraudulent", Status: status},
	}
}

func TestDisputesReachTheDisputeSink(t *testing.T) {
	r := newSaleRig(t)
	r.l.Disputes = r.sink
	res := r.handle(t, disputeEvent("evt_d1", payforge.EventDisputeOpened, "needs_response"))
	if res.Outcome != payforge.OutcomeApplied || res.Dispute == nil || res.Dispute.Closed || res.Dispute.ChargeID != "ch_1" {
		t.Fatalf("opened: %+v", res)
	}
	res = r.handle(t, disputeEvent("evt_d2", payforge.EventDisputeClosed, "lost"))
	if d := res.Dispute; d == nil || !d.Closed || !d.Lost || d.Won {
		t.Fatalf("closed: %+v", res)
	}
	// A late "opened" cannot reopen it, and a repeat is a duplicate.
	r.handle(t, disputeEvent("evt_d0", payforge.EventDisputeOpened, "needs_response"))
	if d, _ := r.sink.Dispute("dp_1"); !d.Closed || !d.Lost {
		t.Fatalf("reopened: %+v", d)
	}
	if res := r.handle(t, disputeEvent("evt_d2", payforge.EventDisputeClosed, "lost")); res.Outcome != payforge.OutcomeDuplicate {
		t.Fatalf("repeat: %+v", res)
	}
	won := r.handle(t, disputeEvent("evt_d3", payforge.EventDisputeClosed, "won"))
	if d := won.Dispute; d == nil || !d.Won || d.Lost {
		t.Fatalf("won: %+v", won)
	}

	// From a connected account, or for another app, it is skipped.
	ev := disputeEvent("evt_d4", payforge.EventDisputeOpened, "needs_response")
	ev.Account = "acct_seller123"
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped {
		t.Fatalf("connected account: %+v", res)
	}
	ev = disputeEvent("evt_d5", payforge.EventDisputeOpened, "needs_response")
	ev.App = "otherapp"
	if res := r.handle(t, ev); res.Outcome != payforge.OutcomeSkipped {
		t.Fatalf("other app: %+v", res)
	}
	// Without a sink it is ignored.
	r2 := newSaleRig(t)
	if res := r2.handle(t, disputeEvent("evt_d1", payforge.EventDisputeOpened, "needs_response")); res.Outcome != payforge.OutcomeIgnored {
		t.Fatalf("no sink: %+v", res)
	}
	// A failed checkout is the host's to record.
	if res := r2.handle(t, payforge.Event{ID: "evt_f", Kind: payforge.EventCheckoutFailed}); res.Outcome != payforge.OutcomeIgnored {
		t.Fatalf("checkout failed: %+v", res)
	}
}
