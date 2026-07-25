# kibana-degradation-runner

`kibana-degradation-runner` creates repeatable local environments for observing how
Kibana behaves when Elasticsearch and cross-cluster search are degraded.

Each YAML scenario describes the required cluster topology, test data, and network
faults. The runner prepares the environment and keeps it active while you navigate
Kibana manually. Kibana remains a separate development process.

## Requirements

- Go 1.26 or newer.
- Native Linux or macOS on x64 or ARM64.
- A bootstrapped Kibana checkout with `yarn` available in `PATH`.
- Internet access the first time Toxiproxy is downloaded.

Windows and WSL are not supported.

By default, the runner expects the repositories to be siblings:

```text
Projects/
├── kibana/
└── kibana-degradation-runner/
```

## Getting started

Clone the repository:

```bash
git clone https://github.com/bartoval/kibana-degradation-runner.git
cd kibana-degradation-runner
```

Validate the example scenario:

```bash
go run ./cmd/kibana-degradation-runner validate scenarios/ccs_slow_remote.yml
```

Start its local environment:

```bash
go run ./cmd/kibana-degradation-runner up scenarios/ccs_slow_remote.yml
```

`validate` checks the YAML structure, required values, target references, and latency
configuration.

`up`:

1. Downloads Toxiproxy 2.12.0 when it is not already cached.
2. Verifies the downloaded binary with its SHA-256 checksum.
3. Starts the Toxiproxy API at `http://127.0.0.1:8474`.
4. Creates a transport proxy on port `9401`.
5. Uses Kibana's `scripts/es.js snapshot` tool to start a remote Elasticsearch on
   ports `9201` and `9301`.
6. Uses the same Kibana tool to start a coordinator on ports `9200` and `9300`.
7. Connects the coordinator to the remote through Toxiproxy in CCS proxy mode.
8. Creates the fixtures declared by the scenario on the selected clusters.
9. Waits for both clusters and the CCS connection, reporting progress every ten seconds.
10. Prints the command for starting Kibana separately against the coordinator.
11. Keeps the environment alive until you press `Ctrl+C`.
12. Stops every process and releases its ports before exiting.

The terminal shows concise progress. Complete output from each child process is stored
under `.runtime/<scenario-id>/logs/`. If Elasticsearch fails or times out, the runner
prints the latest log lines and the full log path.

Network faults are not connected to `up` yet.

## Scenario structure

The example scenario is divided into three sections:

- `topology`: one or more remote Elasticsearch clusters.
- `fixtures`: zero or more test-data presets.
- `faults`: one or more network degradations.

Use [`scenarios/ccs_slow_remote.yml`](scenarios/ccs_slow_remote.yml) as the starting
point for another scenario.

## Runtime files

Downloaded tools and process logs are stored under `.runtime/`, which is ignored by Git:

```text
.runtime/
├── tools/
│   └── toxiproxy/
└── <scenario-id>/
    ├── elasticsearch/
    │   ├── coordinator/
    │   └── remote-1/
    └── logs/
        ├── elasticsearch-coordinator.log
        ├── elasticsearch-remote-1.log
        └── toxiproxy.log
```

## Development checks

```bash
gofmt -w cmd/kibana-degradation-runner/main.go
go vet ./...
go build ./cmd/kibana-degradation-runner
```
