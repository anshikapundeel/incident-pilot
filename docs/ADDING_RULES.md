# Adding a new rule

A rule is a small Go type implementing the `correlate.Rule`
interface. Adding one is a ~80-line change.

## Steps

1. **Create a file** under `internal/rules/` named in snake_case after
   the rule, e.g. `oom_kill.go`.

2. **Write the type:**

   ```go
   package rules

   import (
       "fmt"
       "github.com/anshikapundeel/incident-pilot/internal/model"
   )

   type OOMKill struct {
       // tunables as struct fields so tests can override
       MinCount int
   }

   func NewOOMKill() *OOMKill { return &OOMKill{MinCount: 1} }

   func (r *OOMKill) ID() string    { return "kernel.oom_kill" }
   func (r *OOMKill) Title() string { return "OOM-kill events on alerting service" }

   func (r *OOMKill) Evaluate(a *model.Alert, ctx *model.Context) ([]model.Finding, error) {
       svc := a.Service()
       count := 0
       for _, l := range ctx.RecentLogErrors {
           if l.Service == svc && strings.Contains(l.Message, "oom-killer") {
               count++
           }
       }
       if count < r.MinCount {
           return nil, nil
       }
       return []model.Finding{{
           RuleID:     r.ID(),
           Title:      r.Title(),
           Severity:   model.SeverityHigh,
           Confidence: model.ConfidenceHigh,
           Summary:    fmt.Sprintf("OOM-killer ran %d times for %s in the alert window.", count, svc),
           Suggestion: fmt.Sprintf("Bump memory limit for %s and investigate the leak.\n"+
                                   "  kubectl describe pod -l app=%s | grep -i oom", svc, svc),
           Evidence: []model.Evidence{
               {Text: fmt.Sprintf("%d oom-killer log lines", count), Source: "logs", Value: count},
           },
       }}, nil
   }
   ```

3. **Register it** in `internal/rules/default.go`:

   ```go
   func Default() []correlate.Rule {
       return []correlate.Rule{
           NewDuplicateOpen(),
           NewRecentDeploy(),
           NewErrorSpike(),
           NewSlowTraces(),
           NewTeamRouting(),
           NewOOMKill(),    // ← here
       }
   }
   ```

4. **Add tests** in `internal/rules/rules_test.go`. At minimum:
   - One synthetic case that triggers the rule.
   - One that should NOT trigger it (clean data).
   - One boundary case where the rule should stay quiet (missing
     metric, unrelated service, etc.).

## Rule contract recap

The engine enforces two things:

1. **Every finding must cite at least one Evidence.** Findings
   without evidence are dropped silently with a warning log.
2. **A panicking or erroring rule becomes a LOW finding**, never
   crashes the pipeline. Look for "Rule … errored" entries in your
   incident output; those are bugs in your rule.

## Style notes

- **One file per rule.** Don't share helpers across rules in a
  `_common.go` until you have at least three rules that genuinely
  reuse the same logic. Premature factoring makes rules harder
  to read, not easier.
- **Thresholds go on the struct, not as constants.** Lets tests
  override them without monkeypatching.
- **The `Suggestion` field is plain text with code blocks indented
  two spaces.** The templated renderer passes it through verbatim.
  Aim for something an engineer can copy-paste and run.
- **The `Summary` field should be one paragraph:** what was observed,
  why it matters in one sentence. Don't try to explain the whole
  subsystem here; that's what `References` is for.

## Avoid these anti-patterns

- **Calling out to external services from inside a rule.** Rules
  must be fast (microseconds, not seconds) and deterministic. If
  you need external data, fetch it during enrichment and read from
  `Context`.
- **Using an LLM inside a rule.** That violates the central
  architectural commitment: the rule engine is deterministic. LLMs
  rephrase findings at the end of the pipeline, not during analysis.
- **Returning findings with no concrete `Suggestion`.** If you can't
  describe what to do about the finding, it's an observation, not a
  finding — consider whether it should fire at all.
