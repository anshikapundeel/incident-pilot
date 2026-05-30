package rules

import (
	"fmt"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

// DuplicateOpen catches the "we're already paged about this" case.
// If there's an unresolved incident for the same alert fingerprint or
// for the same service+alertname pair, we surface that *first*.
//
// This is the rule that prevents alert storms from turning into
// page-floods. The on-call engineer sees "incident #142 already open
// for this; new alerts from the same source are duplicates" instead
// of getting paged 30 times.
type DuplicateOpen struct{}

func NewDuplicateOpen() *DuplicateOpen { return &DuplicateOpen{} }

func (r *DuplicateOpen) ID() string    { return "incident.duplicate_open" }
func (r *DuplicateOpen) Title() string { return "Duplicate of an already-open incident" }

func (r *DuplicateOpen) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
	for _, inc := range ctx.OpenIncidents {
		if inc.Resolved {
			continue
		}
		// Exact fingerprint match → definitely the same alert re-firing.
		if a.Fingerprint != "" && inc.Alert.Fingerprint == a.Fingerprint {
			return []model.Finding{{
				RuleID:     r.ID(),
				Title:      r.Title(),
				Severity:   model.SeverityInfo,
				Confidence: model.ConfidenceHigh,
				Summary: fmt.Sprintf(
					"Alert fingerprint %s matches open incident %s. "+
						"Treat this as a re-fire of the existing incident.",
					a.Fingerprint, inc.ID,
				),
				Suggestion: fmt.Sprintf(
					"Update incident %s rather than starting a new investigation.",
					inc.ID,
				),
				Evidence: []model.Evidence{
					{
						Text:   fmt.Sprintf("open incident %s, same fingerprint", inc.ID),
						Source: "store",
						Value:  inc.ID,
					},
				},
			}}, nil
		}
		// Same service + alertname → likely related, lower confidence.
		if inc.Alert.Service() == a.Service() && inc.Alert.Name() == a.Name() {
			return []model.Finding{{
				RuleID:     r.ID(),
				Title:      r.Title(),
				Severity:   model.SeverityLow,
				Confidence: model.ConfidenceMedium,
				Summary: fmt.Sprintf(
					"An incident (%s) is already open for service=%s, "+
						"alert=%s. New alert likely related.",
					inc.ID, a.Service(), a.Name(),
				),
				Suggestion: fmt.Sprintf(
					"Check incident %s; if related, append context there. "+
						"If genuinely a separate problem, mark this one explicitly.",
					inc.ID,
				),
				Evidence: []model.Evidence{
					{
						Text:   fmt.Sprintf("open incident %s for same service/alertname", inc.ID),
						Source: "store",
						Value:  inc.ID,
					},
				},
			}}, nil
		}
	}
	return nil, nil
}
