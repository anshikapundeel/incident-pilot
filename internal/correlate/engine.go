// Package correlate runs rules against an (Alert, Context) pair and
// returns sorted findings. The contract is the same as perf-advisor:
//
//   - Every finding must cite at least one Evidence. Findings without
//     evidence are dropped with a warning. This is the platform's
//     promise that nothing in a report is opinion-only — there's
//     always a data pointer to verify it.
//
//   - A rule that panics or errors does NOT kill the report. The
//     engine converts the failure into a LOW/INFO finding noting the
//     bug, so an operator still gets useful output.
//
// The engine is intentionally small. The interesting code is in the
// rules themselves.
package correlate

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// Rule is the interface every detection rule implements.
//
// ID() must be stable across releases — it shows up in the report and
// in metrics. Title() is the human-readable name.
type Rule interface {
	ID() string
	Title() string
	Evaluate(alert *model.Alert, ctx *model.Context) ([]model.Finding, error)
}

// Engine runs a fixed set of rules against an alert+context.
type Engine struct {
	Rules []Rule
}

// Run evaluates every rule, validates the results, sorts them, and
// returns the final list. Never panics.
func (e *Engine) Run(alert *model.Alert, ctx *model.Context) []model.Finding {
	var out []model.Finding
	for _, r := range e.Rules {
		fs := safeEvaluate(r, alert, ctx)
		for _, f := range fs {
			if len(f.Evidence) == 0 {
				slog.Warn("rule produced finding without evidence; dropping",
					"rule", r.ID(), "title", f.Title)
				continue
			}
			out = append(out, f)
		}
	}
	sortFindings(out)
	return out
}

// safeEvaluate runs one rule with panic recovery, returning a
// degraded finding instead of letting the whole pipeline crash.
func safeEvaluate(r Rule, alert *model.Alert, ctx *model.Context) (out []model.Finding) {
	defer func() {
		if p := recover(); p != nil {
			out = []model.Finding{degraded(r, fmt.Errorf("rule panicked: %v", p))}
		}
	}()
	fs, err := r.Evaluate(alert, ctx)
	if err != nil {
		return []model.Finding{degraded(r, err)}
	}
	return fs
}

func degraded(r Rule, err error) model.Finding {
	return model.Finding{
		RuleID:     r.ID(),
		Title:      fmt.Sprintf("Rule %q errored", r.ID()),
		Severity:   model.SeverityLow,
		Confidence: model.ConfidenceLow,
		Summary:    fmt.Sprintf("The rule raised an error: %s", err.Error()),
		Suggestion: "This is a bug in incident-pilot, not your system. " +
			"Report it via GitHub issues with --debug logs.",
		Evidence: []model.Evidence{{
			Text:   err.Error(),
			Source: "engine",
		}},
	}
}

// sortFindings orders by severity (high first), then confidence
// (high first), then rule ID for stable output.
func sortFindings(f []model.Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		si, sj := f[i].Severity.Rank(), f[j].Severity.Rank()
		if si != sj {
			return si > sj
		}
		ci := confRank[f[i].Confidence]
		cj := confRank[f[j].Confidence]
		if ci != cj {
			return ci > cj
		}
		return f[i].RuleID < f[j].RuleID
	})
}

var confRank = map[model.Confidence]int{
	model.ConfidenceLow:    0,
	model.ConfidenceMedium: 1,
	model.ConfidenceHigh:   2,
}
