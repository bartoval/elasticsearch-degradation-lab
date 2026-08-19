# elasticsearch-degradation-lab

Creates repeatable local Elasticsearch environments with controlled network
degradation and cross-cluster search topologies. Kibana is the primary use case,
but the exposed Elasticsearch endpoints can also be exercised directly or by other
clients. A YAML scenario defines the topology, test data, and network faults.

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

Install it with Go 1.26 or newer:

```bash
go install github.com/bartoval/elasticsearch-degradation-lab/cmd/elasticsearch-degradation-lab@latest
```

This is the recommended way on macOS. A binary compiled locally never acquires the
quarantine attribute, so Gatekeeper does not refuse to open it, which it does for
anything downloaded and not notarized.

Then start the runner in one terminal:

```bash
elasticsearch-degradation-lab up scenarios/fanout100__latency500.yml
```

If Kibana is elsewhere, provide its directory:

```bash
elasticsearch-degradation-lab up scenarios/1remote__slow25s.yml \
  --kibana-dir /path/to/kibana
```

From a clone, without installing:

```bash
go run ./cmd/elasticsearch-degradation-lab up scenarios/fanout100__latency500.yml
```

### Using a release archive on macOS

A downloaded build is not notarized, so macOS refuses it until the quarantine
attribute is removed:

```bash
tar -xzf elasticsearch-degradation-lab-darwin-arm64.tar.gz
xattr -dr com.apple.quarantine elasticsearch-degradation-lab-darwin-arm64
```

Extracting from a terminal rather than from Finder usually avoids the attribute in
the first place.

Start Kibana separately, either before or after the runner, using your normal
development command.

Before reproducing the behavior, wait for:

```text
Scenario "<scenario name>" infrastructure is ready.
```

When Kibana and the scenario are ready, open your local Kibana instance and reproduce
the behavior.

Press `Ctrl+C` in the runner terminal to stop Elasticsearch and Toxiproxy. Stop Kibana
separately. A clean shutdown removes the per-scenario Elasticsearch installation,
data, and internal logs; the operator-facing files under `.runtime/<id>/logs/` and
the shared Toxiproxy cache under `.runtime/tools/` are preserved.

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

The proxy counters cover traffic between Kibana and Elasticsearch (and CCS
transport traffic for alias proxies). They do not measure Kibana-to-browser bytes;
use browser network instrumentation for that leg.

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
profile backs many logical CCS aliases with one physical remote and gives a single
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

- `elasticsearch`: optional tuning of the clusters the runner starts.
- `topology`: an optional physical remote; when present, it has one or more logical
  CCS aliases.
- `fixtures`: zero or more test-data presets.
- `faults`: zero or more network degradations.

The `elasticsearch` block raises limits for scenarios whose subject is a limit
rather than a network condition, such as a result size the defaults refuse to
return:

```yaml
elasticsearch:
  heap: 8g
  settings:
    esql.query.result_truncation_max_size: "1000000"
    indices.breaker.request.limit: "90%"
```

Both clusters receive the same tuning, so that a measurement never depends on which
node answered. Settings are added to the ones the runner already passes and cannot
replace them: names such as `http.port` or `path.data` describe where a node listens
and writes, which the runner has to know to connect the topology and reset it, so a
scenario that sets one is rejected rather than silently ignored.

Setting `aliasCount` on the remote registers the same physical cluster under that
many CCS aliases, numbered from the `alias` prefix:

```yaml
topology:
  remotes:
    - id: remote-1
      alias: serverless-logging
      aliasCount: 100
```

The coordinator then sees that many remote clusters, each reached through its own
Toxiproxy proxy, while only two Elasticsearch processes run locally. A fault
targeting the remote applies to every one of its proxies. The upper bound on
`aliasCount` is the size of the port range the runner reserves for those proxies;
exceeding it is a validation error that names the limit.

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

The `bulk_documents` preset is the opposite case: it indexes real documents into one
index, for scenarios where a query has to return rows rather than resolve metadata.

```yaml
fixtures:
  - target: coordinator
    preset: bulk_documents
    name: logstash-degradation
    documents: 500000
    fieldsPerDocument: 86
    messageBytes: 5370
    timeRange: 24h
```

`timeRange` is optional and distributes the generated timestamps uniformly from
fixture creation backwards across that duration. Without it, documents retain the
original one-second spacing. `fieldsPerDocument` counts generated fields in addition
to the fixed timestamp, message, sequence, service, and level fields.

Each preset accepts only the sizing it reads. Asking `bulk_logs` for `documents`, or
`bulk_documents` for `dataStreams`, is rejected, because a number that was declared
and then ignored is worse than one that was refused.

A large fixture is recreated from a clean data directory on every run, so its cost
is paid at every start. Each scenario file states its own cost in the comment at the
top.

Scenario file names describe the **infrastructure** — topology, fault, fixtures —
rather than a single Kibana screen, because one profile usually serves several
workflows. The catalog in [`scenarios/README.md`](scenarios/README.md) lists every
profile with what it builds and what it is good for.

Validate one without starting any processes:

```bash
./elasticsearch-degradation-lab validate scenarios/fanout100__latency500.yml
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for implementation details and development
commands.
