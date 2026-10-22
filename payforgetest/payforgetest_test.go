package payforgetest_test

import (
	"testing"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/payforgetest"
)

func TestMemoryStoreConforms(t *testing.T) {
	payforgetest.Store(t, func(*testing.T) (payforge.Store, func(string, string)) {
		s := payforge.NewMemoryStore()
		return s, s.Link
	})
}

func TestMemorySinkConforms(t *testing.T) {
	payforgetest.Sink(t, func(*testing.T) (payforge.EntitlementSink, func(string) (payforge.Entitlement, bool)) {
		s := payforge.NewMemorySink()
		return s, s.Get
	})
}
