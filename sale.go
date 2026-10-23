package payforge

import (
	"context"
	"fmt"
)

// handleRefund applies a refund of a one-time sale. Refunds of anything that is
// not a payforge sale, such as a subscription's invoice, are skipped.
func (l *Lifecycle) handleRefund(ctx context.Context, ev Event) (Result, error) {
	if l.Sales == nil {
		return Result{Outcome: OutcomeIgnored, Reason: "no sale sink"}, nil
	}
	if dup, err := l.duplicate(ctx, ev); err != nil {
		return Result{}, err
	} else if dup {
		return Result{Outcome: OutcomeDuplicate}, nil
	}
	if ev.Refund == nil {
		return l.skip(ctx, ev, "the event carries no refund")
	}
	if ev.Account != "" {
		return l.skip(ctx, ev, "event is from a connected account, not the platform")
	}
	if ev.App != "" && ev.App != l.Spec.App {
		return l.skip(ctx, ev, "event is for app "+ev.App)
	}
	if ev.Product == "" {
		return l.skip(ctx, ev, "the charge is not a sale of a product")
	}
	subject, err := l.subject(ctx, ev)
	if err != nil {
		return Result{}, err
	}
	if subject == "" {
		return unknownAccount("no account is known for the sale"), nil
	}
	r := SaleRefund{
		Subject: subject, Seller: ev.Metadata[MetaSeller], Product: ev.Product,
		ChargeID: ev.Refund.ChargeID, PaymentIntent: ev.Refund.PaymentIntent,
		AmountCents: ev.Refund.AmountCents, RefundedCents: ev.Refund.RefundedCents,
		Full: ev.Refund.Full, EventID: ev.ID,
	}
	if err := l.Sales.ApplyRefund(ctx, r); err != nil {
		return Result{}, fmt.Errorf("payforge: apply refund: %w", err)
	}
	if err := l.Store.MarkSeen(ctx, ev.ID); err != nil {
		return Result{}, fmt.Errorf("payforge: record event: %w", err)
	}
	return Result{Outcome: OutcomeApplied, Refund: &r}, nil
}

// handleAccount applies a change to a seller's connected account. With an
// AccountReader the provider's current state is applied, not the event's, so a
// late event cannot turn charges back off.
func (l *Lifecycle) handleAccount(ctx context.Context, ev Event) (Result, error) {
	if l.Sellers == nil {
		return Result{Outcome: OutcomeIgnored, Reason: "no seller sink"}, nil
	}
	if dup, err := l.duplicate(ctx, ev); err != nil {
		return Result{}, err
	} else if dup {
		return Result{Outcome: OutcomeDuplicate}, nil
	}
	if ev.AccountState == nil {
		return l.skip(ctx, ev, "the event carries no account")
	}
	if ev.App != "" && ev.App != l.Spec.App {
		return l.skip(ctx, ev, "event is for app "+ev.App)
	}
	// The seller is named by the metadata payforge put on the account.
	if ev.Subject == "" {
		return l.skip(ctx, ev, "the account was not created through payforge")
	}
	state := *ev.AccountState
	if l.Accounts != nil {
		cur, err := l.Accounts.Account(ctx, state.ID)
		if err != nil {
			return Result{}, fmt.Errorf("payforge: read account: %w", err)
		}
		state = cur
	}
	a := SellerAccount{
		Subject: ev.Subject, AccountID: state.ID, ChargesEnabled: state.ChargesEnabled,
		PayoutsEnabled: state.PayoutsEnabled, DetailsSubmitted: state.DetailsSubmitted, EventID: ev.ID,
	}
	if err := l.Sellers.ApplySeller(ctx, a); err != nil {
		return Result{}, fmt.Errorf("payforge: apply seller: %w", err)
	}
	if err := l.Store.MarkSeen(ctx, ev.ID); err != nil {
		return Result{}, fmt.Errorf("payforge: record event: %w", err)
	}
	return Result{Outcome: OutcomeApplied, Seller: &a}, nil
}

// duplicate reports whether the event was already processed.
func (l *Lifecycle) duplicate(ctx context.Context, ev Event) (bool, error) {
	if ev.ID == "" {
		return false, fmt.Errorf("payforge: %s event has no ID", ev.Type)
	}
	seen, err := l.Store.Seen(ctx, ev.ID)
	if err != nil {
		return false, fmt.Errorf("payforge: check event: %w", err)
	}
	return seen, nil
}

// skip records an event that is relevant but cannot be acted on.
func (l *Lifecycle) skip(ctx context.Context, ev Event, reason string) (Result, error) {
	if err := l.Store.MarkSeen(ctx, ev.ID); err != nil {
		return Result{}, fmt.Errorf("payforge: record event: %w", err)
	}
	return Result{Outcome: OutcomeSkipped, Reason: reason}, nil
}

// unknownAccount is a skip that is not recorded: the host may link the customer
// to an account after this event, and the provider's redelivery should then apply.
func unknownAccount(reason string) Result {
	return Result{Outcome: OutcomeSkipped, Reason: reason}
}
