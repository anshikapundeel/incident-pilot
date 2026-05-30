// Package rules contains the production-ready set of correlation rules
// shipped with incident-pilot v0.
//
// Each rule lives in its own file. The pattern is intentionally close
// to perf-advisor's rules/ package — a class per file, an init wiring
// it into DefaultRules, thresholds as struct fields so tests can tweak
// them without monkeypatching.
package rules

import (
	"fmt"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// RecentDeploy fires when the alert started shortly after a deploy
// to the same service. This is the highest-value correlation rule in
// any incident system: the deploy correlation is the most common true
// cause of fresh alerts, and the suggested action (rollback) is
// concrete and quickly verifiable.
//
// Thresholds based on practical incident response experience:
//   - 10 minutes: very high correlation. Deploy is the obvious suspect.
//   - 60 minutes: medium correlation. Worth mentioning.
//   - > 60 minutes: probably unrelated.
type RecentDeploy struct {
	StrongWindow time.Duration // default 10m
	WeakWindow   time.Duration // default 60m
}

func NewRecentDeploy() *RecentDeploy {
	return &RecentDeploy{
		StrongWindow: 10 * time.Minute,
		WeakWindow:   60 * time.Minute,
	}
}

func (r *RecentDeploy) ID() string    { return "deploy.recent_correlated" }
func (r *RecentDeploy) Title() string { return "Recent deploy correlates with alert" }

func (r *RecentDeploy) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
	svc := a.Service()
	// Find the most recent deploy for this service that preceded the
	// alert. "Preceded" within the weak window. We don't care about
	// deploys after the alert started — those are irrelevant.
	var best *model.Deploy
	var bestGap time.Duration
	for i := range ctx.RecentDeploys {
		d := &ctx.RecentDeploys[i]
		if d.Service != svc {
			continue
		}
		gap := a.StartsAt.Sub(d.When)
		if gap < 0 {
			continue // deploy is after the alert
		}
		if gap > r.WeakWindow {
			continue
		}
		if best == nil || gap < bestGap {
			best = d
			bestGap = gap
		}
	}
	if best == nil {
		return nil, nil
	}

	sev := model.SeverityMedium
	conf := model.ConfidenceMedium
	if bestGap <= r.StrongWindow {
		sev = model.SeverityHigh
		conf = model.ConfidenceHigh
	}

	summary := fmt.Sprintf(
		"Service %q was deployed %s before %q fired. "+
			"Commit %s by %s.",
		svc, formatDuration(bestGap), a.Name(),
		shortCommit(best.Commit), best.Author,
	)

	suggestion := fmt.Sprintf(
		"Investigate the recent deploy first.\n"+
			"  Verify the deploy:\n"+
			"    git show %s\n"+
			"  If correlation looks causal, roll back:\n"+
			"    kubectl rollout undo deployment/%s\n"+
			"  Re-verify within 2 minutes; the alert should clear.",
		shortCommit(best.Commit), svc,
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
				Text:   fmt.Sprintf("deploy at %s (commit %s)", best.When.Format(time.RFC3339), shortCommit(best.Commit)),
				Source: "deploys",
				Value:  best,
			},
			{
				Text:   fmt.Sprintf("alert fired at %s", a.StartsAt.Format(time.RFC3339)),
				Source: "alert",
				Value:  a.StartsAt,
			},
		},
		References: []string{
			"https://sre.google/sre-book/postmortem-culture/",
		},
	}}, nil
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return d.Round(time.Minute).String()
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
