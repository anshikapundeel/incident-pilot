package rules

import (
	"fmt"
	"sort"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// SlowTraces fires when the recent traces for the alerting service
// show a tail-latency anomaly. We compute p99 from the recent samples
// and compare against a fixed threshold; rules that compare against a
// learned baseline are stronger but require the regression-detector
// component (planned as regression-radar).
//
// "Recent" means traces in ctx.RecentTraces, which the enrichment layer
// fetches over a small time window around the alert.
type SlowTraces struct {
	MinSamples    int           // need at least this many samples to fire
	LowP99        time.Duration // p99 above this → LOW
	MediumP99     time.Duration
	HighP99       time.Duration
}

func NewSlowTraces() *SlowTraces {
	return &SlowTraces{
		MinSamples: 20,
		LowP99:     500 * time.Millisecond,
		MediumP99:  2 * time.Second,
		HighP99:    10 * time.Second,
	}
}

func (r *SlowTraces) ID() string    { return "traces.tail_latency" }
func (r *SlowTraces) Title() string { return "Trace p99 latency elevated" }

func (r *SlowTraces) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
	svc := a.Service()
	var durations []time.Duration
	for _, t := range ctx.RecentTraces {
		if t.Service != svc {
			continue
		}
		durations = append(durations, t.Duration)
	}
	if len(durations) < r.MinSamples {
		return nil, nil
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	// 99th percentile via nearest-rank: index = ceil(0.99 * N) - 1
	idx := int(float64(len(durations))*0.99) - 1
	if idx < 0 {
		idx = 0
	}
	p99 := durations[idx]

	if p99 < r.LowP99 {
		return nil, nil
	}

	sev := model.SeverityLow
	conf := model.ConfidenceMedium
	switch {
	case p99 >= r.HighP99:
		sev = model.SeverityHigh
		conf = model.ConfidenceHigh
	case p99 >= r.MediumP99:
		sev = model.SeverityMedium
		conf = model.ConfidenceMedium
	}

	// Find the slowest trace as a follow-the-thread pointer.
	slowest := ctx.RecentTraces[0]
	for _, t := range ctx.RecentTraces {
		if t.Service == svc && t.Duration > slowest.Duration {
			slowest = t
		}
	}

	summary := fmt.Sprintf(
		"Trace p99 for %q across %d recent samples is %s. "+
			"The slowest trace was %s (operation %q).",
		svc, len(durations), p99.Round(time.Millisecond),
		slowest.Duration.Round(time.Millisecond), slowest.Operation,
	)

	suggestion := fmt.Sprintf(
		"Open the slowest trace and walk its spans top-down to find the "+
			"longest non-overlapping child.\n"+
			"  trace_id: %s",
		slowest.TraceID,
	)

	return []model.Finding{{
		RuleID:     r.ID(),
		Title:      r.Title(),
		Severity:   sev,
		Confidence: conf,
		Summary:    summary,
		Suggestion: suggestion,
		Evidence: []model.Evidence{
			{
				Text:   fmt.Sprintf("p99 over %d traces = %s", len(durations), p99),
				Source: "traces",
				Value:  p99,
			},
			{
				Text:   fmt.Sprintf("slowest trace: %s, %s", slowest.TraceID, slowest.Duration),
				Source: "traces",
				Value:  slowest,
			},
		},
	}}, nil
}
