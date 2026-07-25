# kibana-degradation-runner

`kibana-degradation-runner` creates repeatable local environments for observing how
Kibana behaves when Elasticsearch and cross-cluster search are degraded.

Each YAML scenario describes the required cluster topology, test data, and network
faults. The runner prepares the environment and keeps it active while you navigate
Kibana manually. Kibana remains a separate development process.

## Requirements

- Go 1.26 or newer.
- Native Linux or macOS on x64 or ARM64.
- Internet access the first time Toxiproxy is downloaded.

Windows and WSL are not supported.

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

`up` currently:

1. Downloads Toxiproxy 2.12.0 when it is not already cached.
2. Verifies the downloaded binary with its SHA-256 checksum.
3. Starts the Toxiproxy API at `http://127.0.0.1:8474`.
4. Keeps the process alive until you press `Ctrl+C`.
5. Stops Toxiproxy and releases its port before exiting.

Elasticsearch startup, CCS configuration, fixtures, and faults are not connected to
`up` yet.

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
    └── logs/
        └── toxiproxy.log
```

## Development checks

```bash
gofmt -w cmd/kibana-degradation-runner/main.go
go vet ./...
go build ./cmd/kibana-degradation-runner
```
