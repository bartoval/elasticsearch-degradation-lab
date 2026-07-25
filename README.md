# kibana-degradation-runner

Go implementation of the Kibana degradation scenario runner.

Milestone 1 validates YAML scenario files. It does not start Toxiproxy, Elasticsearch,
or Kibana yet.

## Validate a scenario

```bash
go run ./cmd/kibana-degradation-runner --help
go run ./cmd/kibana-degradation-runner validate scenarios/ccs_slow_remote.yml
```

`validate` checks the YAML structure, required values, target references, and latency
configuration. `up` still returns `not implemented yet` with exit code `1`.

## Verify the project

```bash
gofmt -w cmd/kibana-degradation-runner/main.go
go vet ./...
go build ./cmd/kibana-degradation-runner
```
