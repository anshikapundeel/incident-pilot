package rules

import "github.com/anshikapundeel/incident-pilot/internal/correlate"

// Default returns the standard set of rules shipped with v0.
// New rules go here and into their own files. The pattern is identical
// to perf-advisor's DEFAULT_RULES.
func Default() []correlate.Rule {
	return []correlate.Rule{
		NewDuplicateOpen(),
		NewRecentDeploy(),
		NewErrorSpike(),
		NewSlowTraces(),
		NewTeamRouting(),
	}
}
