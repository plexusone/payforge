package limits_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/plexusone/payforge/examples/limits"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

func load(t *testing.T) *pricingspec.Spec {
	t.Helper()
	f, err := os.Open("../catalog/pricing.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	spec, err := pricingspec.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestAHardLimitRefusesPastTheAllowance(t *testing.T) {
	spec := load(t)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if err := limits.AddBoard(spec, "free", 2, log); err != nil {
		t.Fatalf("the third board: %v", err)
	}
	err := limits.AddBoard(spec, "free", 3, log)
	if !errors.Is(err, limits.ErrOverLimit) || !strings.Contains(err.Error(), "3 boards") {
		t.Fatalf("the fourth board: %v", err)
	}
	if err := limits.AddBoard(spec, "business", 10000, log); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	// A plan the account no longer has resolves to the default plan.
	if err := limits.AddBoard(spec, "gone", 3, log); !errors.Is(err, limits.ErrOverLimit) {
		t.Fatalf("unknown plan: %v", err)
	}
}

func TestFeatureLevels(t *testing.T) {
	spec := load(t)
	for _, tc := range []struct {
		plan, level string
		want        bool
	}{{"free", "email", false}, {"plus", "email", true}, {"plus", "priority", false}, {"business", "priority", true}} {
		if got := limits.CanUse(spec, tc.plan, "support", tc.level); got != tc.want {
			t.Errorf("%s support at %s = %v", tc.plan, tc.level, got)
		}
	}
}
