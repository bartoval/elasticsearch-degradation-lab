# Contributing

## Runner lifecycle

The `up` command:

1. Downloads and verifies Toxiproxy when it is not already cached.
2. Starts the Toxiproxy API and creates a transport proxy.
3. Uses Kibana's `scripts/es.js snapshot` tool to start the remote and coordinator
   Elasticsearch clusters.
4. Connects the coordinator to the remote through Toxiproxy in CCS proxy mode.
5. Creates the fixtures and applies the faults declared by the scenario.
6. Keeps the environment active until it receives `Ctrl+C`.
7. Stops every managed process.

## Runtime files

Downloaded tools, Elasticsearch data, logs, and reports are stored under `.runtime/`,
which is ignored by Git:

```text
.runtime/
├── tools/
│   └── toxiproxy/
└── <scenario-id>/
    ├── elasticsearch/
    │   ├── coordinator/
    │   └── remote-1/
    ├── logs/
    │   ├── elasticsearch-coordinator.log
    │   ├── elasticsearch-remote-1.log
    │   └── toxiproxy.log
```

## Development checks

Run the formatting, static analysis, and build checks before submitting a change:

```bash
go fmt ./...
go vet ./...
go build ./cmd/kibana-degradation-runner
```

## Release artifacts

The `Build artifacts` GitHub Actions workflow runs manually or when a `v*` tag is
pushed. It produces archives and SHA-256 checksums for Linux and macOS on x64 and
ARM64.

Each archive contains the executable, the example scenario, `README.md`, and `LICENSE`.
Windows and WSL artifacts are intentionally not produced.
