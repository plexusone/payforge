// Package limits shows how a host enforces a plan's limits: ask the spec, and
// act on the answer.
package limits

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// ErrOverLimit means the plan does not allow the action.
var ErrOverLimit = errors.New("over the plan's limit")

// AddBoard checks creating one more board on an account's plan. used is how
// many boards the account has now. A hard limit refuses; a fair-use limit lets
// the action through and flags the account for a person to look at.
func AddBoard(spec *pricingspec.Spec, plan string, used int, log *slog.Logger) error {
	switch spec.Check(plan, "boards", used+1) {
	case pricingspec.Refuse:
		max, _, _ := spec.Allowance(plan, "boards")
		return fmt.Errorf("%w: the %s plan allows %d boards", ErrOverLimit, spec.PlanOrDefault(plan).Name, max)
	case pricingspec.Warn:
		log.Warn("fair-use limit exceeded", "plan", plan, "limit", "boards", "used", used+1)
	}
	return nil
}

// CanUse reports whether a plan includes a feature at the least a level.
func CanUse(spec *pricingspec.Spec, plan, feature, level string) bool {
	return spec.AtLeast(plan, feature, level)
}
