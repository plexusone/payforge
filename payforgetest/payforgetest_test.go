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

func TestMemorySinkConformsForSales(t *testing.T) {
	payforgetest.Sales(t, func(*testing.T) (payforge.SaleSink, func(string) (payforge.SaleRefund, bool)) {
		s := payforge.NewMemorySink()
		return s, s.Refund
	})
}

func TestMemorySinkConformsForSellers(t *testing.T) {
	payforgetest.Sellers(t, func(*testing.T) (payforge.SellerSink, func(string) (payforge.SellerAccount, bool)) {
		s := payforge.NewMemorySink()
		return s, s.Seller
	})
}
