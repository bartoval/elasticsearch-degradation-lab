# Design

You write a YAML scenario, the runner builds it: Elasticsearch nodes, a few proxies,
some data, and a slow network. Kibana connects as usual and never knows.

```text
Kibana ──► :9200 Toxiproxy ──► :9210 coordinator ES
                                     │ CCS
                                     ▼
                         :9401..9800 Toxiproxy ──► :9301 remote ES
```

The trick: every CCS alias gets its own proxy. So one real remote can pretend to
be hundreds of clusters, and you can slow down all of them or just one.

## What `up` does

1. Reads the scenario and checks it.
2. Starts Toxiproxy and the proxies.
3. Starts the Elasticsearch nodes (using your Kibana checkout).
4. Connects the CCS aliases.
5. Loads the data.
6. Turns on the faults, last, so setup runs on a healthy network.

If something breaks along the way, everything is torn down.

`play` is the same thing in the background; `stop` shuts it down. `Ctrl+C`, `stop`,
or a crashed process all stop everything. Logs stay in `.runtime/<id>/logs/`.

One scenario can run per user. The lock, stop socket, and last result live in the
private directory `/tmp/kibana-scenario-runner-<uid>/`, so long project paths work
and `stop` works from any directory. Data and logs stay in the project.

`play` waits for readiness and shows recent runner output if startup fails.
`stop` waits for cleanup and the lock to be released. If a background process
failed, a later `stop` reports the saved error and log path.

## Where the code is

```text
cmd/kibana-scenario-runner   the CLI
internal/runner              puts the pieces together
internal/elasticsearch       nodes, CCS, data
internal/toxiproxy           proxies and faults
internal/scenario            the YAML
internal/process             child processes
scenarios                    built-in scenarios
```

## Limits

- One remote, up to 400 aliases.
- Local only. Faults are TCP-level, so no per-endpoint faults.
