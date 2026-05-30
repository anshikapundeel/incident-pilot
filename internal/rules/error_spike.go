package rules

import (
	"fmt"
	"strings"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// ErrorSpike fires when there are many ERROR-level log lines for the
// alerting service in a tight window before/around the alert. The
// summary surfaces the most common error message so an on-call engineer
// has a starting point.
//
// The summarisation is deliberately heuristic — we group by the prefix
// of the message (first 80 chars after stripping leading variable
// content) to handle "request 12345 failed" vs "request 67890 failed"
// looking the same. A full log clusterer (drain3, logmine) would be
// stronger but adds dependencies; the heuristic catches the common case.
type ErrorSpike struct {
	LowCount     int  // logs to fire at LOW
	MediumCount  int
	HighCount    int
}

func NewErrorSpike() *ErrorSpike {
	return &ErrorSpike{
		LowCount:    3,
		MediumCount: 10,
		HighCount:   50,
	}
}

func (r *ErrorSpike) ID() string    { return "logs.error_spike" }
func (r *ErrorSpike) Title() string { return "Error-log volume elevated" }

func (r *ErrorSpike) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
	svc := a.Service()
	var relevant []model.LogSample
	for _, l := range ctx.RecentLogErrors {
		if l.Service != svc {
			continue
		}
		if l.Level != "ERROR" && l.Level != "error" && l.Level != "Error" {
			continue
		}
		relevant = append(relevant, l)
	}
	count := len(relevant)
	if count < r.LowCount {
		return nil, nil
	}

	sev := model.SeverityLow
	conf := model.ConfidenceMedium
	switch {
	case count >= r.HighCount:
		sev = model.SeverityHigh
		conf = model.ConfidenceHigh
	case count >= r.MediumCount:
		sev = model.SeverityMedium
		conf = model.ConfidenceMedium
	}

	// Cluster by message prefix; report the most common.
	clusters := map[string]int{}
	for _, l := range relevant {
		key := normalize(l.Message)
		clusters[key]++
	}
	type kv struct {
		key   string
		count int
	}
	var top kv
	for k, c := range clusters {
		if c > top.count {
			top = kv{k, c}
		}
	}

	summary := fmt.Sprintf(
		"%d ERROR-level log entries from %q in the alert window. "+
			"Most common message (×%d): %q",
		count, svc, top.count, top.key,
	)

	suggestion := fmt.Sprintf(
		"Inspect the dominant error.\n"+
			"  kubectl logs deployment/%s --tail=200 | grep -i error\n"+
			"  (or open your log UI filtered to service=%s)",
		svc, svc,
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
				Text:   fmt.Sprintf("%d error-level lines for %s", count, svc),
				Source: "logs",
				Value:  count,
			},
			{
				Text:   fmt.Sprintf("dominant cluster (×%d)", top.count),
				Source: "logs",
				Value:  top.key,
			},
		},
	}}, nil
}

// normalize collapses obvious variable content (numbers, hex IDs) to a
// placeholder so two messages with different IDs cluster together.
// Keep the first 100 chars after normalisation.
func normalize(msg string) string {
	var b strings.Builder
	prevDigit := false
	for _, r := range msg {
		if r >= '0' && r <= '9' {
			if !prevDigit {
				b.WriteString("#")
				prevDigit = true
			}
			continue
		}
		prevDigit = false
		// strip hex-ish characters when they form long runs (uuid-ish)
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}
