package rules

import (
	"fmt"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// TeamRouting is a low-severity informational rule that surfaces the
// team and severity the alert maps to. It doesn't change the
// incident's severity (the engine handles that); it just makes the
// routing decision visible in the report.
//
// Why have it as a rule rather than hard-coding the routing logic?
// Because the rule appears in the incident output as a finding,
// which makes the routing decision auditable. Operators can see
// "this went to team-platform because of these labels" instead of
// having to grep configs.
type TeamRouting struct{}

func NewTeamRouting() *TeamRouting { return &TeamRouting{} }

func (r *TeamRouting) ID() string    { return "routing.team" }
func (r *TeamRouting) Title() string { return "Team routing" }

func (r *TeamRouting) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
	team := a.Team()
	if team == "" {
		return []model.Finding{{
			RuleID:     r.ID(),
			Title:      "Alert has no team label",
			Severity:   model.SeverityLow,
			Confidence: model.ConfidenceHigh,
			Summary: "The alert did not include a `team` label, so the " +
				"platform cannot route automatically. The alert will go " +
				"to the catch-all channel.",
			Suggestion: "Add a `team` label to this alert's rule definition " +
				"so future fires route to the right team automatically.",
			Evidence: []model.Evidence{
				{
					Text:   "no team label on alert",
					Source: "alert",
					Value:  a.Labels,
				},
			},
		}}, nil
	}
	return []model.Finding{{
		RuleID:     r.ID(),
		Title:      r.Title(),
		Severity:   model.SeverityInfo,
		Confidence: model.ConfidenceHigh,
		Summary: fmt.Sprintf(
			"Routing to team %q (severity %s).",
			team, a.AlertSeverity(),
		),
		Suggestion: "",
		Evidence: []model.Evidence{
			{
				Text:   fmt.Sprintf("team=%s", team),
				Source: "alert",
				Value:  team,
			},
		},
	}}, nil
}
