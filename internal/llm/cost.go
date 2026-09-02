package llm

import (
	"sync"
	"time"
)

type CostEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

type CostSummary struct {
	TotalInputTokens  int         `json:"total_input_tokens"`
	TotalOutputTokens int         `json:"total_output_tokens"`
	TotalCostUSD      float64     `json:"total_cost_usd"`
	Entries           []CostEntry `json:"entries"`
}

// pricing per million tokens, declared by each provider through
// SetModelPricing. The core ships no prices of its own: a model it has never
// heard of is simply counted as free.
var (
	pricingMu    sync.RWMutex
	modelPricing = map[string][2]float64{}
)

type CostTracker struct {
	mu      sync.Mutex
	entries []CostEntry
}

func NewCostTracker() *CostTracker {
	return &CostTracker{}
}

func (ct *CostTracker) Track(resp *Response) CostEntry {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	entry := CostEntry{
		Timestamp:    time.Now(),
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      calculateCost(resp.Model, resp.InputTokens, resp.OutputTokens),
	}
	ct.entries = append(ct.entries, entry)
	return entry
}

// TrackDirect records a cost entry with a pre-computed cost (e.g. voice API calls).
func (ct *CostTracker) TrackDirect(model string, costUSD float64) CostEntry {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	entry := CostEntry{
		Timestamp: time.Now(),
		Model:     model,
		CostUSD:   costUSD,
	}
	ct.entries = append(ct.entries, entry)
	return entry
}

func (ct *CostTracker) Summary() CostSummary {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	var s CostSummary
	s.Entries = make([]CostEntry, len(ct.entries))
	copy(s.Entries, ct.entries)
	for _, e := range ct.entries {
		s.TotalInputTokens += e.InputTokens
		s.TotalOutputTokens += e.OutputTokens
		s.TotalCostUSD += e.CostUSD
	}
	return s
}

func (ct *CostTracker) Reset() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.entries = nil
}

func calculateCost(model string, inputTokens, outputTokens int) float64 {
	pricingMu.RLock()
	pricing, ok := modelPricing[model]
	pricingMu.RUnlock()
	if !ok {
		return 0
	}
	inputCost := float64(inputTokens) / 1_000_000 * pricing[0]
	outputCost := float64(outputTokens) / 1_000_000 * pricing[1]
	return inputCost + outputCost
}

// SetModelPricing declares what a model costs per million input and output
// tokens. Providers call it for the models they serve.
func SetModelPricing(model string, inputPerMillion, outputPerMillion float64) {
	pricingMu.Lock()
	defer pricingMu.Unlock()
	modelPricing[model] = [2]float64{inputPerMillion, outputPerMillion}
}
