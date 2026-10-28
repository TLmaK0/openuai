package llm

import "testing"

// A cost reported by the provider wins over the token estimate, and an
// unknown model id no longer silently costs zero when the provider knows it.
func TestTrackUsesReportedCost(t *testing.T) {
	ct := NewCostTracker()
	ct.Track(&Response{Model: "some-unpriced-model", InputTokens: 2, OutputTokens: 100, CostUSD: 0.0112})
	if got := ct.Summary().TotalCostUSD; got != 0.0112 {
		t.Fatalf("got %v, want 0.0112", got)
	}
	SetModelPricing("priced-test-model", 1, 1)
	ct.Reset()
	ct.Track(&Response{Model: "priced-test-model", InputTokens: 1_000_000})
	if got := ct.Summary().TotalCostUSD; got != 1 {
		t.Fatalf("fallback to token price: got %v, want 1", got)
	}
}
