# kibana-degradation-runner

Creates repeatable local environments for observing Kibana under degraded
Elasticsearch and cross-cluster search conditions. A YAML scenario defines the
topology, test data, and network faults.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for a high-level view of the components and
request flow.

## Try it

You need native Linux or macOS and a bootstrapped Kibana checkout. Use the Node.js
version pinned by Kibana in `.nvmrc`; the runner invokes that checkout's
`scripts/es.js` with the `node` executable in your `PATH`. Windows and WSL are not
supported.

By default, the runner looks for Kibana at `../kibana`, relative to the directory
where you run the command. Use `--kibana-dir` when the checkout is elsewhere.

Start the runner in one terminal:

```bash
./kibana-degradation-runner up scenarios/fanout100__latency500.yml
```

If Kibana is elsewhere, provide its directory:

```bash
./kibana-degradation-runner up scenarios/1remote__slow25s.yml \
  --kibana-dir /path/to/kibana
```

Alternatively, run it from source with Go 1.26 or newer:

```bash
go run ./cmd/kibana-degradation-runner up scenarios/fanout100__latency500.yml
```

Start Kibana separately, either before or after the runner, using your normal
development command.

Before reproducing the behavior, wait for:

```text
Scenario "<scenario name>" infrastructure is ready.
```

When Kibana and the scenario are ready, open your local Kibana instance and reproduce
the behavior.

Press `Ctrl+C` in the runner terminal to stop Elasticsearch and Toxiproxy. Stop Kibana
separately.

When they are not already cached, the runner downloads Toxiproxy and Kibana's
matching Elasticsearch snapshot. Later runs reuse both caches.

### Ports

With Kibana's standard development configuration, no additional Elasticsearch
setting is needed: Kibana already connects to `http://localhost:9200`. That address
is a proxy rather than Elasticsearch itself, so every Elasticsearch request from
Kibana can receive the faults declared by the scenario. A custom
`elasticsearch.hosts` setting must still point to port 9200.

| Port | What answers |
| --- | --- |
| 9200 | proxy in front of the coordinator, which is what Kibana connects to |
| 9210 | the coordinator itself |
| 9201 | remote HTTP endpoint, when the scenario has a remote |
| 9300 | coordinator transport endpoint |
| 9301 | remote transport endpoint, when configured |
| 9401-9800 | one transport proxy per CCS alias |
| 8474 | the Toxiproxy API, with per-proxy byte counters at `/metrics` |

The applicable ports must be free before startup. A coordinator-only scenario uses
9200, 9210, 9300, and 8474; scenarios with a remote also use the remaining ports.

The two doors differ only in whether the coordinator's own proxy is crossed. A fault
on a remote sits *behind* the coordinator, so a cross-cluster request pays it either
way:

```text
                  ┌─ 9200, through the coordinator proxy ─┐
  you / Kibana ───┤                                        ├─► coordinator ─► alias proxies ─► remote
                  └─ 9210, straight to the coordinator ────┘                   9401-9800
                                                                        remote faults apply here
```

Measure on 9210, so that the coordinator proxy is not part of the number. Use 9200
when the coordinator proxy is itself what you want to measure, which is the case
only when a fault targets `coordinator`; the difference between the two ports is
then the cost that fault adds to every request Kibana makes.

Direct Elasticsearch requests require the development credentials:

```bash
curl -u elastic:changeme http://127.0.0.1:9210/_cluster/health
```

## Compare broad and narrow queries

The [`fanout100__tail25s__bulk50.yml`](scenarios/fanout100__tail25s__bulk50.yml)
profile creates 100 logical CCS aliases backed by one physical remote and gives one
alias high tail latency. Compare its broad source pattern:

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
`.runtime/<scenario-id>/logs/`, relative to the directory where the runner was
started.

## Scenarios

A scenario contains:

- `topology`: an optional physical remote; when present, it has one or more logical
  CCS aliases.
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
Toxiproxy proxy on ports 9401-9800, while only two Elasticsearch processes run
locally. A fault targeting the remote applies to every one of its proxies. The
limit of 400 aliases comes from that reserved port range.

Because cross-cluster requests fan out in parallel, a uniform latency does not
accumulate across aliases: it costs roughly one slow round trip, not one per alias.
Alias count does, however, multiply cluster-qualified `_resolve/index` results.
`_field_caps` response size instead grows primarily with the distinct field names
created by the fixture.

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

`dataStreams` grows what `_resolve/index` returns for each alias, while `aliasCount`
multiplies those cluster-qualified results. `fieldsPerStream` grows the
`_field_caps` union and the field list Kibana has to hold. Field names are distinct
per data stream because `_field_caps` merges by name; streams sharing one schema
would collapse into a small response. No documents are indexed because both
endpoints read mappings only.

The `resolve2000` profiles create 2,000 data streams from a clean data directory on
every run. Start with the 100-alias profile: fixture creation can take several
minutes and the larger sweep points require substantial local memory.

Scenario file names describe the **infrastructure** (topology, fault, fixtures),
not a single Kibana screen. One profile often supports many workflows — see the
full catalog in [`scenarios/README.md`](scenarios/README.md).

Examples:

- [`1remote__slow25s.yml`](scenarios/1remote__slow25s.yml) — one remote at 25s
  latency; starting point for new profiles.
- [`fanout100__latency500.yml`](scenarios/fanout100__latency500.yml) — 100 CCS
  aliases with uniform wide-area latency.
- [`fanout100__tail25s__bulk50.yml`](scenarios/fanout100__tail25s__bulk50.yml) —
  100 aliases, heavy metadata, one slow tail region.
- [`fanout400__resolve2000.yml`](scenarios/fanout400__resolve2000.yml) — largest
  source-resolution sweep point, without a network fault.
- [`1remote__coord300_remote3s.yml`](scenarios/1remote__coord300_remote3s.yml) —
  small coordinator delay plus larger CCS delay; makes duplicate requests visible.

Validate it without starting any processes:

```bash
./kibana-degradation-runner validate scenarios/fanout100__latency500.yml
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for implementation details and development
commands.
