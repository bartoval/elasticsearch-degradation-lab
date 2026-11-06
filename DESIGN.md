# Design

A scenario describes topology, data, and faults; the runner turns it into local
processes and proxy routes. The Kibana checkout is used to launch matching
Elasticsearch snapshots (`scripts/es.js`) and to load ES archives. Kibana itself is
never started or touched.

```text
Kibana ──► :9200 Toxiproxy ──► :9210 coordinator ES
                                     │ CCS, one alias per proxy
                                     ▼
                         :9401..9800 Toxiproxy ──► :9301 remote ES
```

Every alias gets its own proxy, so one physical remote can pose as hundreds of
clusters, and a fault can hit all of them or just one.

## Startup

1. Load and validate the scenario. Unknown fields fail here.
   Archive fixtures also check their directories, Node.js, and the checkout's
   `EsArchiver` imports and `dataOnly` option before any infrastructure is started.
   This constructs the loader without loading data or contacting Elasticsearch.
2. Start Toxiproxy (downloaded and cached on first use).
3. Create the coordinator proxy and one proxy per alias.
4. Start the remote (if any), then the coordinator; wait for both to be healthy.
5. Register the CCS aliases and wait until all of them are connected.
6. Apply fixtures in YAML order, directly on each node.
7. Apply faults last, so setup never runs against a degraded network.

If any step fails, everything already started is stopped.

## Faults

Faults are Toxiproxy toxics on the response direction. Latency adds a fixed delay;
bandwidth caps throughput, so the delay grows with the response size. Toxiproxy
works at TCP level: it cannot see HTTP paths or headers.

## Lifecycle

The runner watches every process it started. `Ctrl+C`, `SIGTERM`, or an unexpected
exit triggers shutdown in reverse start order (coordinator, remote, Toxiproxy). A
clean stop deletes the per-run Elasticsearch data and keeps the logs; a failed stop
keeps everything for inspection.

## Code layout

```text
cmd/kibana-scenario-runner          CLI, exit codes, output
internal/runner                     startup order, ports, supervision
internal/elasticsearch              nodes, REST client, CCS, fixtures
internal/toxiproxy                  binary, server, proxies, toxics
internal/scenario                   YAML schema and validation
internal/process                    child processes and signals
scenarios                           built-in scenarios, embedded in the binary
```

A new fixture or fault usually touches `scenario/types.go` + `validate.go`, the
implementation in `elasticsearch/` or `toxiproxy/`, and the wiring in
`runner/steps.go`.

## Limits

- At most one physical remote; up to 400 aliases (the proxy port range).
- Local only. Kibana and the measurements stay with whoever runs it.
