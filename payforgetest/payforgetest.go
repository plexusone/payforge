// Package payforgetest holds conformance suites for the ports a host implements:
// run them against your Store and EntitlementSink to check they behave the way
// the lifecycle relies on.
package payforgetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/plexusone/payforge"
)

// Store checks a payforge.Store. make returns a fresh, empty store and a
// function that records the host's account for a provider customer, which the
// Store interface leaves to the host.
func Store(t *testing.T, make func(t *testing.T) (s payforge.Store, link func(customerID, subject string))) {
	t.Helper()
	ctx := context.Background()

	t.Run("an event is unseen until marked", func(t *testing.T) {
		s, _ := make(t)
		if seen, err := s.Seen(ctx, "evt_1"); err != nil || seen {
			t.Fatalf("Seen = %v, %v", seen, err)
		}
		if err := s.MarkSeen(ctx, "evt_1"); err != nil {
			t.Fatal(err)
		}
		if seen, err := s.Seen(ctx, "evt_1"); err != nil || !seen {
			t.Fatalf("Seen after MarkSeen = %v, %v", seen, err)
		}
	})
	t.Run("marking twice is harmless and events are independent", func(t *testing.T) {
		s, _ := make(t)
		for i := 0; i < 2; i++ {
			if err := s.MarkSeen(ctx, "evt_1"); err != nil {
				t.Fatalf("MarkSeen #%d: %v", i+1, err)
			}
		}
		if seen, _ := s.Seen(ctx, "evt_2"); seen {
			t.Fatal("marking one event marked another")
		}
	})
	t.Run("an unknown customer is ErrNotFound", func(t *testing.T) {
		s, _ := make(t)
		if _, err := s.SubjectForCustomer(ctx, "cus_unknown"); !errors.Is(err, payforge.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("a linked customer finds its account", func(t *testing.T) {
		s, link := make(t)
		link("cus_1", "org-1")
		link("cus_2", "org-2")
		for cus, want := range map[string]string{"cus_1": "org-1", "cus_2": "org-2"} {
			if got, err := s.SubjectForCustomer(ctx, cus); err != nil || got != want {
				t.Fatalf("%s: %q, %v", cus, got, err)
			}
		}
	})
	t.Run("concurrent use is safe", func(t *testing.T) {
		s, _ := make(t)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				id := fmt.Sprintf("evt_%d", i%5)
				if err := s.MarkSeen(ctx, id); err != nil {
					t.Error(err)
				}
				if _, err := s.Seen(ctx, id); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		for i := 0; i < 5; i++ {
			if seen, _ := s.Seen(ctx, fmt.Sprintf("evt_%d", i)); !seen {
				t.Errorf("evt_%d lost", i)
			}
		}
	})
}

// Sink checks a payforge.EntitlementSink. make returns a fresh sink and a
// function that reads back the entitlement the host holds for an account.
func Sink(t *testing.T, make func(t *testing.T) (s payforge.EntitlementSink, get func(subject string) (payforge.Entitlement, bool))) {
	t.Helper()
	ctx := context.Background()
	end := time.Unix(1795000000, 0).UTC()
	granted := payforge.Entitlement{
		Subject: "org-1", Plan: "plus", Granted: true, SubscribedPlan: "plus", Status: "active",
		ValidUntil: end, SubscriptionID: "sub_1", CustomerID: "cus_1", Origin: payforge.OriginSubscription, EventID: "evt_1",
	}

	t.Run("an applied entitlement is held", func(t *testing.T) {
		s, get := make(t)
		if _, ok := get("org-1"); ok {
			t.Fatal("a new sink holds an entitlement")
		}
		if err := s.Apply(ctx, granted); err != nil {
			t.Fatal(err)
		}
		got, ok := get("org-1")
		if !ok || got.Plan != "plus" || !got.Granted || got.SubscriptionID != "sub_1" || !got.ValidUntil.Equal(end) {
			t.Fatalf("held %+v", got)
		}
	})
	t.Run("applying the same entitlement twice leaves the same state", func(t *testing.T) {
		s, get := make(t)
		for i := 0; i < 2; i++ {
			if err := s.Apply(ctx, granted); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := get("org-1"); got.Plan != "plus" || !got.Granted || got.EventID != "evt_1" {
			t.Fatalf("held %+v", got)
		}
	})
	t.Run("a later entitlement replaces the earlier, including a drop to the default plan", func(t *testing.T) {
		s, get := make(t)
		_ = s.Apply(ctx, granted)
		dropped := payforge.Entitlement{Subject: "org-1", Plan: "free", Granted: false, SubscribedPlan: "plus", Status: "canceled", Origin: payforge.OriginSubscription, EventID: "evt_2"}
		if err := s.Apply(ctx, dropped); err != nil {
			t.Fatal(err)
		}
		if got, _ := get("org-1"); got.Plan != "free" || got.Granted || got.Status != "canceled" {
			t.Fatalf("held %+v", got)
		}
	})
	t.Run("accounts are independent", func(t *testing.T) {
		s, get := make(t)
		other := granted
		other.Subject, other.Plan = "org-2", "business"
		_ = s.Apply(ctx, granted)
		_ = s.Apply(ctx, other)
		if a, _ := get("org-1"); a.Plan != "plus" {
			t.Fatalf("org-1 holds %+v", a)
		}
		if b, _ := get("org-2"); b.Plan != "business" {
			t.Fatalf("org-2 holds %+v", b)
		}
	})
}
