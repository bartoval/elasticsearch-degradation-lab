# kibana-degradation-runner

Creates repeatable local environments for observing Kibana under degraded
Elasticsearch and cross-cluster search conditions. A YAML scenario defines the
topology, test data, and network faults.

## Try it

You need native Linux or macOS and a bootstrapped Kibana checkout. The runner uses
`../kibana` by default. Windows and WSL are not supported.

Start the runner in one terminal:

```bash
./kibana-degradation-runner up scenarios/ccs_slow_remote.yml
```

If Kibana is elsewhere, provide its directory:

```bash
./kibana-degradation-runner up scenarios/ccs_slow_remote.yml \
  --kibana-dir /path/to/kibana
```

Alternatively, run it from source with Go 1.26 or newer:

```bash
go run ./cmd/kibana-degradation-runner up scenarios/ccs_slow_remote.yml
```

Start Kibana separately, either before or after the runner, using your normal
development command.

Before reproducing the behavior, wait for:

```text
Scenario "Slow CCS remote" infrastructure is ready.
```

When Kibana and the scenario are ready, open your local Kibana instance and reproduce
the behavior.

Press `Ctrl+C` in the runner terminal to stop Elasticsearch and Toxiproxy. Stop Kibana
separately.

The first run requires internet access to download Toxiproxy.

## Result

The example starts one coordinator and one remote Elasticsearch cluster, creates test
data, and delays remote responses by 25 seconds. Coordinator-only requests remain
unaffected.

Elasticsearch and Toxiproxy logs are stored under
`.runtime/ccs_slow_remote/logs/`.

## Scenarios

A scenario contains:

- `topology`: one remote cluster in the current MVP.
- `fixtures`: zero or more test-data presets.
- `faults`: one or more network degradations.

Start from [`scenarios/ccs_slow_remote.yml`](scenarios/ccs_slow_remote.yml).

Validate it without starting any processes:

```bash
./kibana-degradation-runner validate scenarios/ccs_slow_remote.yml
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for implementation details and development
commands.
