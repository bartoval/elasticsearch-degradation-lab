# kibana-degradation-runner

Go implementation of the Kibana degradation scenario runner.

Milestone 0 contains only the command-line stub. It does not read YAML or start Toxiproxy,
Elasticsearch, or Kibana yet.

## Run the stub

```bash
go run ./cmd/kibana-degradation-runner --help
go run ./cmd/kibana-degradation-runner validate scenarios/example.yml
go run ./cmd/kibana-degradation-runner up scenarios/example.yml
```

`validate` and `up` currently return `not implemented yet` with exit code `1`.

## Verify the project

```bash
gofmt -w cmd/kibana-degradation-runner/main.go
go vet ./...
go build ./cmd/kibana-degradation-runner
```
