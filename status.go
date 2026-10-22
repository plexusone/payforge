package payforge

// Subscription statuses, as the provider reports them.
const (
	StatusActive            = "active"
	StatusTrialing          = "trialing"
	StatusPastDue           = "past_due"
	StatusUnpaid            = "unpaid"
	StatusCanceled          = "canceled"
	StatusIncomplete        = "incomplete"
	StatusIncompleteExpired = "incomplete_expired"
	StatusPaused            = "paused"
)

// Grants reports whether a subscription in this status grants its plan.
//
// A subscription grants while it is active or in a trial, and while it is past
// due: the provider is still retrying the payment, and cutting access during
// the retries would punish a customer whose card is being fixed. Every other
// status, and any status this library does not know, does not grant.
func Grants(status string) bool {
	switch status {
	case StatusActive, StatusTrialing, StatusPastDue:
		return true
	}
	return false
}
