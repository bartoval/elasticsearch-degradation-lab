# Contributing

## Runner lifecycle

The `up` command:

1. Downloads and verifies Toxiproxy when it is not already cached.
2. Starts the Toxiproxy API and creates the coordinator proxy plus any CCS alias
   proxies required by the topology.
3. Uses Kibana's `scripts/es.js snapshot` tool to start the optional remote and the
   coordinator Elasticsearch clusters.
4. When a remote is configured, connects its aliases to the coordinator through
   Toxiproxy in CCS proxy mode.
5. Creates the fixtures and applies the faults declared by the scenario.
6. Keeps the environment active until it receives `Ctrl+C`.
7. Stops every managed process.

## Runtime files

Toxiproxy, isolated Elasticsearch installations, data, and logs are stored under
`.runtime/`, relative to the directory where the runner starts. The directory is
ignored by Git:

```text
.runtime/
├── tools/
│   └── toxiproxy/
└── <scenario-id>/
    ├── elasticsearch/
    │   ├── coordinator/
    │   └── remote-1/       # present only for remote scenarios
    ├── logs/
    │   ├── elasticsearch-coordinator.log
    │   ├── elasticsearch-remote-1.log  # present only for remote scenarios
    │   └── toxiproxy.log
```

Elasticsearch snapshot archives are shared with the selected Kibana checkout at
`<kibana-dir>/.es/cache`. Scenario data directories are reset on every `up` run.

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
ARM64. Manual runs keep their files as workflow-run artifacts. Tag runs also create
a GitHub release for the tag and attach every archive and checksum to it.

Each archive contains the executable, every scenario, `README.md`, `ARCHITECTURE.md`,
`CONTRIBUTING.md`, and `LICENSE`. Windows and WSL artifacts are intentionally not
produced.
