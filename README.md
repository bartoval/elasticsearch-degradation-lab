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

### Ports

Kibana needs no configuration, because the address it already expects is the one to
use. That address is a proxy rather than Elasticsearch itself, so that every request
Kibana makes can be observed and delayed, and so that none of them can bypass the
proxy by accident.

| Port | What answers |
| --- | --- |
| 9200 | proxy in front of the coordinator, which is what Kibana connects to |
| 9210 | the coordinator itself |
| 9201 | the remote |
| 9401-9500 | one proxy per CCS alias |
| 8474 | the Toxiproxy API, with per-proxy byte counters at `/metrics` |

The two doors differ only in whether the coordinator's own proxy is crossed. A fault
on a remote sits *behind* the coordinator, so a cross-cluster request pays it either
way:

```text
                  ┌─ 9200, through the coordinator proxy ─┐
  you / Kibana ───┤                                        ├─► coordinator ─► alias proxies ─► remote
                  └─ 9210, straight to the coordinator ────┘                   9401-9500
                                                                        remote faults apply here
```

Measure on 9210, so that the coordinator proxy is not part of the number. Use 9200
when the coordinator proxy is itself what you want to measure, which is the case
only when a fault targets `coordinator`; the difference between the two ports is
then the cost that fault adds to every request Kibana makes.

## Compare broad and narrow queries

The overview scenario creates 100 logical CCS aliases backed by one physical remote
and gives one alias high tail latency. Compare the broad source pattern from the
overview cluster:

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

Setting `aliasCount` on the remote registers the same physical cluster under that
many CCS aliases, numbered from the `alias` prefix:

```yaml
topology:
  remotes:
    - id: remote-1
      alias: serverless-logging
      aliasCount: 100
```

The coordinator then sees 100 remote clusters, each reached through its own
Toxiproxy proxy on ports 9401-9500, while only two Elasticsearch processes run
locally. A fault targeting the remote applies to every one of its proxies. The
limit of 100 aliases comes from that reserved port range.

Because cross-cluster requests fan out in parallel, a uniform latency does not
accumulate across aliases: it costs roughly one slow round trip, not one per
alias. These scenarios reproduce the breadth of the fan-out, which is what makes
`_field_caps`, index resolution, and the default ES|QL query expensive.

A fault targets either a remote or `coordinator`. Targeting the coordinator degrades
what Kibana asks Elasticsearch directly, saved objects included, which is how the
cost of a page load can be told apart from the cost of crossing to the remotes.
By default a remote fault applies to all of its aliases. Set `aliasPosition` to a
1-based position to degrade only one alias, which models the tail latency of one
slow region without slowing the rest of the topology.

The `bulk_logs` preset creates many logs data streams, each with its own field names,
for scenarios where the metadata is the expensive part:

```yaml
fixtures:
  - target: remote-1
    preset: bulk_logs
    name: logs-fanout
    dataStreams: 200
    fieldsPerStream: 300
```

`dataStreams` sets how much index resolution costs, since that is what
`_resolve/index` returns. `fieldsPerStream` sets how much `_field_caps` costs and how
large a field list Kibana has to hold; the names are distinct per data stream because
`_field_caps` merges by name, so streams sharing one schema would collapse into a
single small response no matter how many existed. No documents are indexed, since
both endpoints read mappings only.

- [`ccs_slow_remote.yml`](scenarios/ccs_slow_remote.yml) adds 25 seconds of remote
  latency for testing loading and cancellation behavior.
- [`ccs_overview_fanout.yml`](scenarios/ccs_overview_fanout.yml) reproduces a broad
  query across 100 CCS aliases.
- [`overview_cluster.yml`](scenarios/overview_cluster.yml) reproduces a deployment
  connected to many remotes with one slow tail, including the Kibana settings that
  make Discover choose the broad default query.
- [`discover_reload_cost.yml`](scenarios/discover_reload_cost.yml) puts a small delay
  on every request Kibana makes, so that what Discover re-requests on each navigation
  becomes visible and countable.

Validate it without starting any processes:

```bash
./kibana-degradation-runner validate scenarios/ccs_overview_fanout.yml
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for implementation details and development
commands.
