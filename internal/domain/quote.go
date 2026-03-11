// Package domain contains pure value types and sentinel errors for the carrier
// gateway. It has zero external dependencies — only stdlib is permitted here.
package domain

import "time"

// CoverageLine is a typed string identifying a line of business.
type CoverageLine string

const (
	// CoverageLineAuto identifies automobile insurance coverage.
	CoverageLineAuto CoverageLine = "auto"
	// CoverageLineHomeowners identifies homeowners insurance coverage.
	CoverageLineHomeowners CoverageLine = "homeowners"
	// CoverageLineUmbrella identifies umbrella (excess liability) coverage.
	CoverageLineUmbrella CoverageLine = "umbrella"
)

// Money represents a monetary amount in a specific currency.
// Amount is expressed in the smallest unit (cents) to avoid float precision issues.
type Money struct {
	// Amount is the monetary value in cents (e.g., 150000 = $1,500.00).
	Amount int64
	// Currency is an ISO 4217 currency code (e.g., "USD").
	Currency string
}

// Coverage describes a single priced coverage item within a quote.
type Coverage struct {
	// Line is the line of business this coverage item applies to.
	Line CoverageLine
	// Label is a human-readable description of the coverage (e.g., "Bodily Injury Liability").
	Label string
	// Limit is the maximum payout amount for this coverage.
	Limit Money
	// Deductible is the out-of-pocket amount before coverage applies.
	Deductible Money
}

// QuoteRequest is the inbound domain type for requesting carrier quotes.
type QuoteRequest struct {
	// RequestID is a caller-supplied unique identifier for this quote request.
	RequestID string
	// CoverageLines is the set of lines of business to price.
	CoverageLines []CoverageLine
	// Timeout is the maximum duration to wait for carrier responses.
	Timeout time.Duration
}

// QuoteResult is the normalised output from a single carrier quote.
type QuoteResult struct {
	// RequestID echoes the originating QuoteRequest.RequestID.
	RequestID string
	// CarrierID is the unique identifier of the carrier that produced this result.
	CarrierID string
	// Premium is the total quoted premium across all requested coverage lines.
	Premium Money
	// Coverages is the breakdown of individual priced coverage items.
	Coverages []Coverage
	// ExpiresAt is the time after which this quote is no longer valid.
	ExpiresAt time.Time
	// Latency is the round-trip time from fan-out to result receipt.
	Latency time.Duration
	// IsHedged is true when this result was produced by the hedge monitor,
	// not the primary carrier goroutine.
	IsHedged bool
}
