# kibana-scenario-runner

A local scenario runner for Kibana development. One YAML file combines
Elasticsearch setup, data preparation, and network faults, so developers can
recreate the same test environment with one command.

A scenario can combine:

- A coordinator and an optional remote with many cross-cluster search (CCS) aliases.
- Generated documents, fields, and data streams, or data loaded through Kibana's
  `EsArchiver` and multiplied into a larger index.
- Latency or bandwidth limits on the coordinator, all remote aliases, or selected
  alias connections.

For example, [this scenario](scenarios/fanout20__tail3s__archive10.yml) loads a
Kibana data archive, copies one index ten times, exposes the remote through 20 CCS
aliases, and adds three seconds of latency to one connection.

The runner manages startup, readiness, logs, and cleanup. Start Kibana separately
and use it against the prepared environment; other Elasticsearch clients can use
the same endpoints. See [DESIGN.md](DESIGN.md) for how the pieces fit together.

## Quick start

Linux or macOS, plus a bootstrapped Kibana checkout. The checkout provides the
matching Elasticsearch snapshot; use its Node.js version (`nvm use`).

Get the binary in one of three ways:

- **Release:** download the archive for your platform from the
  [latest release](https://github.com/bartoval/elasticsearch-degradation-lab/releases/latest).
  On macOS the binary is unsigned; if it is blocked, run
  `xattr -d com.apple.quarantine <binary>`.
- **Go install:**
  `go install github.com/bartoval/elasticsearch-degradation-lab/cmd/kibana-scenario-runner@latest`
- **From source:** `go run ./cmd/kibana-scenario-runner` (Go 1.26+).

Then start a scenario:

```bash
kibana-scenario-runner play 1remote__slow25s --kibana-dir /path/to/kibana
```

`--kibana-dir` defaults to `../kibana`. `play` waits until the infrastructure is
ready, then returns while the scenario keeps running in the background. Start
Kibana as usual (`pnpm start`) against that environment.

Stop the scenario from any working directory:

```bash
kibana-scenario-runner stop
```

`stop` waits for Elasticsearch and Toxiproxy to shut down and cleanup to finish;
Kibana is yours to stop. You can pass a scenario name or YAML path to stop only
that scenario. One scenario can run per user because the ports are shared.

Use `up` instead of `play` to keep the runner in the foreground and stop it with
`Ctrl+C`. During `play` startup, `Ctrl+C` cancels preparation and cleans up.

## Ports

Kibana's default `http://localhost:9200` already points at the runner, so no Kibana
configuration is needed.

| Port | What answers |
| --- | --- |
| 9200 | Toxiproxy in front of the coordinator (what Kibana uses) |
| 9210 | the coordinator directly, bypassing its proxy |
| 9201 / 9301 | remote HTTP / transport, when the scenario has a remote |
| 9300 | coordinator transport |
| 9401-9800 | one proxy per CCS alias |
| 8474 | Toxiproxy API, byte counters at `/metrics` |

Remote faults sit behind the coordinator, so CCS requests pay them on either 9200
or 9210. Use 9210 to measure without the coordinator proxy. Credentials are
`elastic:changeme`:

```bash
curl -u elastic:changeme http://127.0.0.1:9210/_cluster/health
```

## Scenarios

The built-in catalog is in [scenarios/README.md](scenarios/README.md). Pass a name
for a built-in scenario or a path for your own YAML, and check it without starting
anything:

```bash
kibana-scenario-runner validate fanout100__latency500
kibana-scenario-runner validate ./my__scenario.yml
```

A scenario file, here `fanout20__example.yml`, looks like this:

```yaml
version: 1
id: fanout20_example          # filename stem with __ replaced by _
name: Fan-out 20, one slow region

elasticsearch:                # optional, applied to every node
  heap: 4g
  settings:
    indices.breaker.request.limit: "90%"

topology:
  remotes:                    # zero or one physical remote
    - id: remote-1
      alias: region           # region-01 .. region-20
      aliasCount: 20
      skipUnavailable: true

fixtures:                     # run in order, before faults
  - target: remote-1
    preset: minimal_logs
    name: logs-degradation-default

faults:                       # applied once everything is ready
  - id: slow-region
    target: remote-1          # a remote or coordinator
    aliasPosition: 20         # optional, 1-based; omit to hit every alias
    type: latency
    latencyMs: 8000
```

### Fixtures

| Preset | Creates | Options |
| --- | --- | --- |
| `minimal_logs` | a logs data stream with one document | — |
| `minimal_lookup` | a lookup index with one document | — |
| `bulk_logs` | many data streams with distinct fields, mappings only | `dataStreams`, `fieldsPerStream` |
| `bulk_documents` | one index with generated documents | `documents`, `fieldsPerDocument`, `messageBytes`, `timeRange` |
| `es_archive` | the indices from a Kibana ES archive | `archive` (path relative to the Kibana checkout) |
| `multiply_documents` | index `name` holding `copies` copies of index `source` | `source`, `copies` |

Options a preset does not read are rejected. `es_archive` uses the checkout's
`EsArchiver` in `dataOnly` mode, so it needs bootstrapped `node_modules` but not a
running Kibana. `multiply_documents` needs a concrete source index created by an
earlier fixture (no aliases or data streams).

For archive fixtures, `play` and `up` check the directory, Node.js, and the
checkout's API before starting Toxiproxy or Elasticsearch. Archive fixture names
must be unique across targets because each writes `logs/fixture-<name>.log`.
Loader credentials are passed through its environment. `validate` checks YAML only.

### Faults

| Type | Effect | Options |
| --- | --- | --- |
| `latency` | fixed delay on responses | `latencyMs`, `jitterMs` |
| `bandwidth` | caps response throughput | `rateKbps` |

## Logs and cleanup

Scenario data and logs live under `.runtime/` in the directory you run from. Logs are in
`.runtime/<id>/logs/`; `play` writes startup progress and runner output to
`runner.log` there. Logs survive a clean stop; the per-run Elasticsearch data is
deleted. Toxiproxy is cached in `.runtime/tools/`, and the Elasticsearch snapshot
in the Kibana checkout's `.es/cache`.

Control files live in the private directory `/tmp/kibana-scenario-runner-<uid>/`,
so `stop` can find the scenario from another directory. If a background component
fails, the runner cleans up and saves the failure there; the next `stop` reports
the cause and log path. A timeout while waiting for `stop` leaves shutdown running
in the background.

## Development

```bash
go fmt ./... && go vet ./... && go test ./...
for scenario in scenarios/*.yml; do
  go run ./cmd/kibana-scenario-runner validate "$scenario" || break
done
```

Pushing a `v*` tag builds Linux and macOS archives and publishes a GitHub release.
Windows and WSL are not supported.
