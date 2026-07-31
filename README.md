# kibana-degradation-runner

Creates repeatable local environments for observing Kibana under degraded
Elasticsearch and cross-cluster search conditions. A YAML scenario defines the
topology, test data, and network faults.

## Try it

You need native Linux or macOS and a bootstrapped Kibana checkout. The runner uses
`../kibana` by default. Windows and WSL are not supported.

Start the runner in one terminal:

```bash
./kibana-degradation-runner up scenarios/ccs_overview_fanout.yml
```

If Kibana is elsewhere, provide its directory:

```bash
./kibana-degradation-runner up scenarios/ccs_slow_remote.yml \
  --kibana-dir /path/to/kibana
```

Alternatively, run it from source with Go 1.26 or newer:

```bash
go run ./cmd/kibana-degradation-runner up scenarios/ccs_overview_fanout.yml
```

Start Kibana separately, either before or after the runner, using your normal
development command.

Before reproducing the behavior, wait for:

```text
Scenario "Overview CCS fan-out" infrastructure is ready.
```

When Kibana and the scenario are ready, open your local Kibana instance and reproduce
the behavior.

Press `Ctrl+C` in the runner terminal to stop Elasticsearch and Toxiproxy. Stop Kibana
separately.

The first run requires internet access to download Toxiproxy.

## Compare broad and narrow queries

The overview scenario creates 100 logical CCS aliases backed by one physical remote
and adds five seconds of latency to CCS traffic. Compare the broad source pattern from
the overview cluster:

```esql
FROM logs-*-*,logs-*,filebeat-*,serverless-logging-*:logs-* | LIMIT 10
```

with a query targeting only the local fixture:

```esql
FROM logs-degradation-default | LIMIT 10
```

The broad query crosses the delayed CCS connection. The local query does not.
In Chrome DevTools, `/internal/esql/autocomplete/sources/all` can be observed
separately because it may still enumerate remote sources for either query.

Elasticsearch and Toxiproxy logs are stored under
`.runtime/<scenario-id>/logs/`.

## Scenarios

A scenario contains:

- `topology`: one physical remote with one or more logical CCS aliases.
- `fixtures`: zero or more test-data presets.
- `faults`: zero or more network degradations.

- [`ccs_overview_fanout.yml`](scenarios/ccs_overview_fanout.yml) reproduces a broad
  query across 100 CCS aliases.
- [`ccs_slow_remote.yml`](scenarios/ccs_slow_remote.yml) adds 25 seconds of remote
  latency for testing loading and cancellation behavior.

Validate it without starting any processes:

```bash
./kibana-degradation-runner validate scenarios/ccs_overview_fanout.yml
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for implementation details and development
commands.
