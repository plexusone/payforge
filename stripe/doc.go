// Package stripe is payforge's Stripe adapter. Client implements
// payforge.Billing against the Stripe API using the official stripe-go library.
//
// Everything written to Stripe carries the spec's app in metadata
// (payforge.MetaApp), so several products can share one account, and every
// creating call carries an idempotency key. A secret key is passed to New and is
// never logged or returned in an error.
package stripe
