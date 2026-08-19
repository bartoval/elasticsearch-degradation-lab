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

Toxiproxy, isolated Elasticsearch installations, data, and logs are created under
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
`<kibana-dir>/.es/cache`. A clean stop removes the per-scenario `elasticsearch/`
directory and preserves `logs/` and `.runtime/tools/`. If a component does not stop
cleanly, the complete scenario directory remains available for inspection.

## Working on the code

Go 1.26 or newer, and one dependency beyond the standard library. Activate the
Node.js version from the Kibana checkout with `nvm use`, then run these commands
from the root of this repository without installing anything:

```bash
go run ./cmd/elasticsearch-degradation-lab validate scenarios/1remote__slow25s.yml
go run ./cmd/elasticsearch-degradation-lab up scenarios/1remote__slow25s.yml
```

`validate` starts no processes, so it is the fast loop while changing the schema.
`up` needs the bootstrapped Kibana checkout and takes minutes, so it is the slow
one. Pass `--kibana-dir /path/to/kibana` unless the checkout is at `../kibana`
relative to this repository.

### Where things live

```text
cmd/elasticsearch-degradation-lab   arguments, exit codes, what to print
internal/runner                     startup order, ports, when a session is over
internal/elasticsearch              node lifecycle, REST client, CCS and fixtures
internal/toxiproxy                  the binary, the server, proxies and toxics
internal/scenario                   the YAML schema and its rules
internal/process                    signals and process groups
scenarios                           YAML sources and the embedded standard catalog
```

Dependencies point one way: `cmd` resolves the embedded catalog and knows `runner`
and `scenario`; `runner` knows the three packages below it; `scenario` and `process`
know nothing. Keeping that direction is what stops presentation decisions from
reaching the code that supervises processes.

### Adding to a scenario

Most changes touch three files in the same order:

1. `internal/scenario/types.go` and `validate.go` — the field, and a rule that rejects
   a value the runner cannot honour. Prefer rejecting over ignoring: a number that
   was declared and then silently dropped is worse than one that was refused.
2. The package that performs it — the matching `fixture_*.go` file in
   `internal/elasticsearch`, or `internal/toxiproxy/proxy.go` for a toxic.
3. `internal/runner/steps.go` — the wiring, plus the progress line that says what is
   about to happen and how large it is.

Then a scenario file that uses it, a row in `scenarios/README.md`, and the schema
paragraph in the main `README.md`.

### Conventions

Errors are wrapped with `%w` and read as a sentence from the outside in, so that a
failure names the step before the cause. Comments explain why a line exists rather
than what it does; the numbers that describe a particular experiment belong in the
scenario file that runs it, not in the code or the README, since only the scenario
can be rerun to check them.

## Development checks

Run the formatting, static analysis, and build checks before submitting a change:

```bash
go fmt ./...
go vet ./...
go build ./cmd/elasticsearch-degradation-lab
```

Every scenario should still validate, which catches a schema change that the files
were not updated for:

```bash
for scenario in scenarios/*.yml; do
  go run ./cmd/elasticsearch-degradation-lab validate "$scenario" || break
done
```

## Release artifacts

The `Build artifacts` GitHub Actions workflow runs manually or when a `v*` tag is
pushed. It produces archives and SHA-256 checksums for Linux and macOS on x64 and
ARM64. Manual runs keep their files as workflow-run artifacts. Tag runs also create
a GitHub release for the tag and attach every archive and checksum to it.

Each archive contains the executable, readable copies of every embedded scenario,
`README.md`, `ARCHITECTURE.md`, `CONTRIBUTING.md`, and `LICENSE`. Windows and WSL
artifacts are intentionally not produced.
