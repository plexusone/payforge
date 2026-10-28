package stripe

import "strings"

// Mode is whether a key, and the events for it, are Stripe's test mode or live
// mode. Objects and events of one mode are never visible to the other.
type Mode string

const (
	// ModeTest is Stripe's test mode: keys that start sk_test_ or rk_test_.
	ModeTest Mode = "test"
	// ModeLive is Stripe's live mode: real money.
	ModeLive Mode = "live"
)

// KeyMode returns the mode of a secret or restricted key. Only a key that starts
// sk_test_ or rk_test_ is test mode; any other key is treated as live, so an
// unrecognized key is never mistaken for one that cannot move money.
func KeyMode(secretKey string) Mode {
	if strings.HasPrefix(secretKey, "sk_test_") || strings.HasPrefix(secretKey, "rk_test_") {
		return ModeTest
	}
	return ModeLive
}

// livemode is the value of an event's livemode field for the mode.
func (m Mode) livemode() bool { return m == ModeLive }

func (m Mode) valid() bool { return m == ModeTest || m == ModeLive }
