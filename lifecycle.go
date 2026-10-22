package payforge

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// Outcome says what Handle did with an event.
type Outcome string

const (
	// OutcomeApplied: the sink was told the account's entitlement.
	OutcomeApplied Outcome = "applied"
	// OutcomeDuplicate: the event was already processed; nothing changed.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeIgnored: the event is not one the lifecycle acts on.
	OutcomeIgnored Outcome = "ignored"
	// OutcomeSkipped: the event is relevant but cannot be acted on, such as one
	// for another product or for a customer no account is known for. Retrying it
	// would not change that, so the host should acknowledge it and may log Reason.
	OutcomeSkipped Outcome = "skipped"
)

// Result is what Handle did.
type Result struct {
	Outcome Outcome
	// Reason explains an ignored or skipped event.
	Reason string
	// Entitlement is what was applied, for OutcomeApplied.
	Entitlement *Entitlement
}

// Lifecycle turns webhook events into entitlements. It holds no state of its own:
// the Store remembers events, and the provider is the source of truth for
// subscriptions.
type Lifecycle struct {
	Spec *pricingspec.Spec
	// Store remembers processed events and maps customers to accounts.
	Store Store
	// Sink receives the account's entitlement.
	Sink EntitlementSink
	// Provider, when set, is read for the customer's subscriptions instead of
	// trusting the event's, so a late or repeated event cannot undo a newer
	// state: after any subscription event the account gets what the provider's
	// subscriptions say now. Without it the event alone decides, and an event that
	// arrives out of order can leave a stale entitlement. Set it in production.
	Provider SubscriptionReader
	// LegacyPrefixes are host metadata prefixes read as aliases for the subject
	// and plan, as in the webhook reader.
	LegacyPrefixes []string
}

// Handle processes one event. It returns an error only when something failed that
// a retry could fix: the store, the provider or the sink. The host answers such
// an error with a 5xx so the provider redelivers, and everything else with a 2xx.
//
// An event is recorded as seen only after the sink has applied it, so a failed
// attempt is retried and a repeated one is a no-op.
func (l *Lifecycle) Handle(ctx context.Context, ev Event) (Result, error) {
	if l.Spec == nil || l.Store == nil || l.Sink == nil {
		return Result{}, errors.New("payforge: lifecycle needs a spec, a store and a sink")
	}
	switch ev.Kind {
	case EventSubscriptionCreated, EventSubscriptionUpdated, EventSubscriptionEnded:
	case EventCheckoutCompleted, EventInvoicePaid, EventInvoiceFailed:
		// A subscription's checkout and renewals are applied from the
		// subscription's own events, which follow; one-time sales and refunds
		// are the host's.
		return Result{Outcome: OutcomeIgnored, Reason: "applied from the subscription events"}, nil
	default:
		return Result{Outcome: OutcomeIgnored, Reason: "not a subscription event"}, nil
	}
	if ev.Subscription == nil {
		return Result{}, fmt.Errorf("payforge: %s event %s has no subscription", ev.Type, ev.ID)
	}

	if seen, err := l.Store.Seen(ctx, ev.ID); err != nil {
		return Result{}, fmt.Errorf("payforge: check event: %w", err)
	} else if seen {
		return Result{Outcome: OutcomeDuplicate}, nil
	}
	skip := func(reason string) (Result, error) {
		if err := l.Store.MarkSeen(ctx, ev.ID); err != nil {
			return Result{}, fmt.Errorf("payforge: record event: %w", err)
		}
		return Result{Outcome: OutcomeSkipped, Reason: reason}, nil
	}

	if ev.App != "" && ev.App != l.Spec.App {
		return skip("event is for app " + ev.App)
	}
	subject, err := l.subject(ctx, ev)
	if err != nil {
		return Result{}, err
	}
	if subject == "" {
		return skip("no account is known for the subscription")
	}

	subs := []SubscriptionState{*ev.Subscription}
	if l.Provider != nil && ev.CustomerID != "" {
		listed, err := l.Provider.Subscriptions(ctx, ev.CustomerID)
		if err != nil {
			return Result{}, fmt.Errorf("payforge: read subscriptions: %w", err)
		}
		subs = listed
	} else if ev.Kind == EventSubscriptionEnded {
		// Without the provider, an ended subscription must not grant whatever
		// status the event carried.
		subs[0].Status = StatusCanceled
	}

	e := l.entitlement(subject, ev, subs)
	if err := l.Sink.Apply(ctx, e); err != nil {
		return Result{}, fmt.Errorf("payforge: apply entitlement: %w", err)
	}
	if err := l.Store.MarkSeen(ctx, ev.ID); err != nil {
		return Result{}, fmt.Errorf("payforge: record event: %w", err)
	}
	return Result{Outcome: OutcomeApplied, Entitlement: &e}, nil
}

// subject resolves the account: the event's metadata first, then the host's
// mapping from the provider customer. Empty with no error means unknown.
func (l *Lifecycle) subject(ctx context.Context, ev Event) (string, error) {
	if ev.Subject != "" {
		return ev.Subject, nil
	}
	if ev.CustomerID == "" {
		return "", nil
	}
	s, err := l.Store.SubjectForCustomer(ctx, ev.CustomerID)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("payforge: find account: %w", err)
	}
	return s, nil
}

// candidate is a subscription that names one of the spec's plans.
type candidate struct {
	sub  SubscriptionState
	plan pricingspec.Plan
	rank int
}

// entitlement decides what the account is on from its subscriptions: the
// highest plan among those that grant, the later period end breaking a tie, or
// the default plan when none grants. It does not depend on the order the
// subscriptions or the events came in.
func (l *Lifecycle) entitlement(subject string, ev Event, subs []SubscriptionState) Entitlement {
	def := l.Spec.PlanOrDefault("")
	e := Entitlement{Subject: subject, Plan: def.ID, CustomerID: ev.CustomerID, Origin: OriginSubscription, EventID: ev.ID}

	var granting, other []candidate
	for _, s := range subs {
		plan, ok := l.planOf(s)
		if !ok {
			continue // a price the spec does not know never grants
		}
		c := candidate{sub: s, plan: plan, rank: l.Spec.TierRank(plan.Tier)}
		if Grants(s.Status) {
			granting = append(granting, c)
		} else {
			other = append(other, c)
		}
	}
	best := func(cs []candidate) candidate {
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].rank != cs[j].rank {
				return cs[i].rank > cs[j].rank
			}
			if !cs[i].sub.PeriodEnd.Equal(cs[j].sub.PeriodEnd) {
				return cs[i].sub.PeriodEnd.After(cs[j].sub.PeriodEnd)
			}
			return cs[i].sub.ID < cs[j].sub.ID
		})
		return cs[0]
	}
	switch {
	case len(granting) > 0:
		c := best(granting)
		e.Plan, e.Granted, e.SubscribedPlan = c.plan.ID, true, c.plan.ID
		e.Status, e.ValidUntil, e.SubscriptionID = c.sub.Status, c.sub.PeriodEnd, c.sub.ID
	case len(other) > 0:
		// Nothing grants: report the best of what does not, for information.
		c := best(other)
		e.SubscribedPlan, e.Status, e.SubscriptionID = c.plan.ID, c.sub.Status, c.sub.ID
		e.ValidUntil = c.sub.PeriodEnd
	}
	return e
}

// planOf resolves a subscription to a plan of the spec. A price's lookup key
// names the plan and interval for a price payforge synced, and counts only if
// the spec has that price. A price without a key, from before payforge, is named
// by the subscription's metadata, and counts only for a self-serve plan. A key for
// another app, a plan or interval the spec does not have, or an arranged plan
// resolves to nothing, so a price made by hand can never grant. With several
// lines the highest plan wins.
func (l *Lifecycle) planOf(s SubscriptionState) (pricingspec.Plan, bool) {
	var best pricingspec.Plan
	bestRank, found := 0, false
	consider := func(p pricingspec.Plan) {
		if r := l.Spec.TierRank(p.Tier); !found || r > bestRank {
			best, bestRank, found = p, r, true
		}
	}
	for _, it := range s.Items {
		if app, id, iv, ok := ParsePlanLookupKey(it.LookupKey); ok {
			if p, has := l.Spec.Plan(id); has && app == l.Spec.App && hasPrice(p, iv) {
				consider(p)
			}
			continue
		}
		if id := MetadataPlan(s.Metadata, l.LegacyPrefixes); id != "" {
			if p, has := l.Spec.Plan(id); has && p.SelfServe && len(p.Prices) > 0 {
				consider(p)
			}
		}
	}
	return best, found
}

func hasPrice(p pricingspec.Plan, iv pricingspec.Interval) bool {
	for _, pr := range p.Prices {
		if pr.Interval == iv {
			return true
		}
	}
	return false
}
