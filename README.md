# incident-pilot

An **AI-augmented incident response service** in Go. Receives
Alertmanager-shaped alerts, correlates them with recent deploys, logs,
and traces via a deterministic rule engine, produces a structured
incident with cited evidence, and optionally rephrases it with a
locally-configured LLM. Routes the result to Slack (or stdout).

This is the runnable implementation of the design in
[`ai-ops-design`](https://github.com/anshikapundeel/ai-ops-design).
The architecture, failure modes, threat model, and cost analysis live
there; this repo is the v0 code that brings the design to life.

```
   Alertmanager webhook ──> incident-pilot ──> Slack / stdout
                                │
                          rule engine + LLM
                                │
                         (cited findings →
                          rephrased prose)
```

**Pure Go stdlib. Zero runtime dependencies. No API keys baked in.**

---

## Why this exists

When an alert fires at 3 AM, the on-call engineer needs three things,
in this order:

1. **Has this just happened?** (a deploy, a config change, a partial
   outage of a dependency)
2. **Is this already being worked on?** (a duplicate of an open incident)
3. **What's the most likely fix?**

incident-pilot answers all three before the page arrives. It does this
without using an LLM for the analysis (LLMs hallucinate causal claims
on time-pressure data; see the [llm-integration design doc](https://github.com/anshikapundeel/ai-ops-design/blob/main/docs/08-llm-integration.md)).
The analysis is deterministic rule code. The LLM, if configured,
rephrases the rule output into prose.

## Features

- **Alertmanager-compatible webhook** at `POST /alerts`. Existing
  Prometheus setups can route to it without changes.
- **5 correlation rules** in v0: recent-deploy correlation,
  error-log spike clustering, trace tail-latency, duplicate
  open-incident detection, team-routing decision.
- **Pluggable enrichment** — push deploys / traces / logs into
  `/context/*` endpoints; rules pick up the data on the next alert.
- **Templated narration always works.** LLM narration is optional
  and gated behind explicit env vars.
- **Slack output** via incoming webhook URL (env-driven; no secret
  in code).
- **Rule contract enforced:** every finding cites at least one
  Evidence; broken rules become a LOW finding rather than crashing
  the pipeline.
- **Race-detector clean** under concurrent ingest + query.

## Build and run

Requires Go 1.22+.

```sh
git clone https://github.com/anshikapundeel/incident-pilot
cd incident-pilot
go build -o incident-pilot ./cmd/incident-pilot

./incident-pilot                  # listens on :9099
```

## Quick demo

In one terminal:

```sh
./incident-pilot
```

In another terminal — record a deploy, feed in some error logs,
then fire the alert:

```sh
# Tell the platform a deploy happened 3 minutes ago
curl -X POST localhost:9099/context/deploys -d '{
  "service": "api-gateway",
  "commit":  "a39bf2c",
  "author":  "alice",
  "when":    "'$(date -u -d "3 minutes ago" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -v-3M +"%Y-%m-%dT%H:%M:%SZ")'"
}'

# Add some error logs
for i in 1 2 3 4 5 6 7 8 9 10; do
  curl -X POST localhost:9099/context/logs -d "{
    \"service\":\"api-gateway\",\"level\":\"ERROR\",
    \"message\":\"upstream timeout request $i\"
  }"
done

# Fire the alert
curl -X POST localhost:9099/alerts \
  -H 'Content-Type: application/json' \
  -d @examples/sample_alert.json

# See the structured incident
curl -s "localhost:9099/api/incidents?limit=1" | jq .
```

In the first terminal you'll see the templated incident report
appear, with a deploy-correlation finding (HIGH severity, since the
deploy was 3 minutes before the alert) and an error-spike finding
(MEDIUM, with the clustered error pattern).

## The rules shipped in v0

| Rule ID                       | What it catches |
|-------------------------------|----------------|
| `deploy.recent_correlated`    | Alert started shortly after a deploy to the same service. Strongest single signal. |
| `logs.error_spike`            | Many ERROR-level log lines for the alerting service; surfaces the dominant error pattern by clustering. |
| `traces.tail_latency`         | p99 latency over recent traces for the service exceeds threshold. |
| `incident.duplicate_open`     | Same fingerprint, or same service+alertname, already has an open incident. Prevents alert-storm page-floods. |
| `routing.team`                | Surfaces the routing decision as a visible finding so it's auditable. |

Each rule lives in its own file in `internal/rules/`, and is ~100
lines of obvious Go. Adding a sixth rule is a small change — see
[ADDING_RULES.md](docs/ADDING_RULES.md).

Each finding includes:
- `severity` (info / low / medium / high / critical)
- `confidence` (low / medium / high) — orthogonal to severity
- `summary` — one paragraph
- `suggestion` — concrete next command
- `evidence` — pointer back to the data that triggered it

## API

| Endpoint | Purpose |
|----------|---------|
| `POST /alerts` | Alertmanager webhook (envelope or bare alert) |
| `POST /context/deploys` | Record a deploy event |
| `POST /context/traces` | Record a trace sample (TraceID, service, duration) |
| `POST /context/logs` | Record an error log line |
| `GET /api/incidents?limit=N` | List recent incidents |
| `GET /api/incidents/{id}` | Fetch one incident in full |
| `POST /api/incidents/{id}/resolve` | Mark an incident resolved |
| `GET /api/stats` | Counters: alerts in, incidents opened, etc. |
| `GET /healthz` | Liveness probe |

## Optional LLM narration

incident-pilot ships with **no API keys, no default provider, and no
outbound calls.** Templated narration is always used. To enable an
LLM rephrasing layer, set environment variables before starting:

```sh
# Local Ollama (recommended for data-sensitive environments):
export INCIDENT_PILOT_LLM_PROVIDER=ollama
export INCIDENT_PILOT_LLM_BASE_URL=http://localhost:11434
export INCIDENT_PILOT_LLM_MODEL=llama3.1:8b
./incident-pilot

# Or any OpenAI-compatible endpoint:
export INCIDENT_PILOT_LLM_PROVIDER=openai-compatible
export INCIDENT_PILOT_LLM_BASE_URL=https://your-endpoint.example.com
export INCIDENT_PILOT_LLM_MODEL=gpt-4o-mini
export INCIDENT_PILOT_LLM_API_KEY=YOUR_KEY
./incident-pilot
```

The LLM is **strictly a presentation layer**. The system prompt
explicitly forbids the model from introducing new findings, changing
severity, or inventing actions. The structured incident (the
authoritative data) is always preserved alongside the narration.

When the LLM is unavailable or fails, incident-pilot silently falls
back to templated narration. The alert still gets routed.

For the full reasoning behind this design see
[`ai-ops-design/docs/08-llm-integration.md`](https://github.com/anshikapundeel/ai-ops-design/blob/main/docs/08-llm-integration.md).

## Slack output

Set `INCIDENT_PILOT_SLACK_WEBHOOK` to a Slack incoming-webhook URL:

```sh
export INCIDENT_PILOT_SLACK_WEBHOOK=https://hooks.slack.com/services/T.../B.../...
./incident-pilot
```

The Slack sink is added in addition to (not instead of) the stdout
sink. Use `-quiet` to suppress stdout if you only want Slack.

## What this is and isn't

**Is:**

- A complete, runnable incident-correlation service. Drop it in,
  point Alertmanager at `/alerts`, push deploys into `/context/deploys`,
  and you get structured incident reports on every fire.
- A reference implementation of the
  [ai-ops-design](https://github.com/anshikapundeel/ai-ops-design)
  intelligence-plane subsystem, specifically the incident pipeline
  documented in
  [docs/02-incident-pipeline.md](https://github.com/anshikapundeel/ai-ops-design/blob/main/docs/02-incident-pipeline.md).
- A demonstration that AI-augmented incident response can be built
  with a deterministic core and an *optional* LLM layer, rather than
  an LLM-as-judge that hallucinates causal claims.

**Isn't:**

- A storage backend. Incidents live in memory; they're lost on
  restart. Production deployments would back the store with Postgres
  or similar.
- An alert manager. Use Prometheus Alertmanager (or any other) for
  rule evaluation. incident-pilot starts where Alertmanager hands off.
- A search engine. Querying past incidents by content is not
  implemented. The full design covers this via vector retrieval
  (see ai-ops-design); not in v0.
- A full observability stack. Pair it with
  [`flow-trace`](https://github.com/anshikapundeel/flow-trace)
  for tracing,
  [`sched-trace`](https://github.com/anshikapundeel/sched-trace) for
  kernel telemetry,
  [`redfish-exporter`](https://github.com/anshikapundeel/redfish-exporter)
  for hardware events, and
  [`perf-advisor`](https://github.com/anshikapundeel/perf-advisor) for
  performance rule analysis.

## Project layout

```
internal/model/       Alert, Context, Finding, Incident — domain types
internal/correlate/   Rule engine: runs rules, enforces evidence contract
internal/rules/       Five v0 detection rules (one file each)
internal/store/       In-memory incident store, sync.RWMutex-guarded
internal/narrate/     Templated narration + optional LLM hook
internal/route/       Stdout + Slack delivery sinks
internal/server/      HTTP API (alerts, context, query endpoints)

cmd/incident-pilot/   Server binary

examples/             Sample alert + deploy JSON payloads
docs/                 DESIGN.md, ADDING_RULES.md
```

## Tests

```sh
go test ./...                   # 27 tests, all green
go test -race ./...             # also race-clean
```

CI matrix: Go 1.22 and 1.23, both with `-race`, plus an end-to-end
smoke that fires an alert against a live server and verifies the
incident shows up.

## Roadmap

- [x] Alertmanager-shape ingest, bare-alert fallback
- [x] 5 correlation rules with cited evidence
- [x] Optional LLM hook (Ollama / OpenAI-compatible)
- [x] Slack + stdout delivery sinks
- [x] In-memory store + REST query API
- [x] 27 unit + e2e tests, race-clean
- [ ] Past-incident retrieval (vector similarity over resolved incidents)
- [ ] Postgres-backed store for durability
- [ ] Hallucination-check on LLM output (claim → finding grounding)
- [ ] PagerDuty / Opsgenie sinks
- [ ] Prometheus `/metrics` endpoint
- [ ] More rules: cgroup OOM, leader-election thrash, dependency-graph cascade

## License

MIT.
