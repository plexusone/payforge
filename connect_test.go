package payforge_test

import (
	"errors"
	"math"
	"os"
	"testing"

	"github.com/plexusone/payforge"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

func TestFee(t *testing.T) {
	for _, tc := range []struct {
		amount int64
		bps    int
		want   int64
	}{
		{0, 1000, 0}, {1000, 0, 0}, {1000, 1000, 100}, {499, 800, 39}, {499, 500, 24},
		{1, 9999, 0}, {1, 10000, 1}, {math.MaxInt64 / 10000, 10000, math.MaxInt64 / 10000},
	} {
		got, err := payforge.Fee(tc.amount, tc.bps)
		if err != nil || got != tc.want {
			t.Errorf("Fee(%d, %d) = %d, %v; want %d", tc.amount, tc.bps, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		amount int64
		bps    int
	}{{-1, 100}, {100, -1}, {100, 10001}, {math.MaxInt64, 5000}} {
		if _, err := payforge.Fee(tc.amount, tc.bps); err == nil {
			t.Errorf("Fee(%d, %d) accepted", tc.amount, tc.bps)
		}
	}
}

func TestFeeBps(t *testing.T) {
	data, err := os.ReadFile("testdata/marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := pricingspec.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for plan, want := range map[string]int{"seller": 800, "pro_seller": 500, "browser": 1000, "": 1000, "gone": 1000} {
		if got, err := payforge.FeeBps(spec, plan); err != nil || got != want {
			t.Errorf("FeeBps(%q) = %d, %v; want %d", plan, got, err, want)
		}
	}
	spec.Marketplace = nil
	if _, err := payforge.FeeBps(spec, "seller"); !errors.Is(err, payforge.ErrNoMarketplace) {
		t.Errorf("no marketplace: %v", err)
	}
}
