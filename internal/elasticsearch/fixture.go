package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	minimalLogsPreset   = "minimal_logs"
	minimalLookupPreset = "minimal_lookup"
)

// ApplyFixture creates the small data preset selected by the scenario.
func (node *Node) ApplyFixture(ctx context.Context, preset string) error {
	switch preset {
	case minimalLogsPreset:
		return node.applyMinimalLogs(ctx)
	case minimalLookupPreset:
		return node.applyMinimalLookup(ctx)
	default:
		return fmt.Errorf("unknown fixture preset %q", preset)
	}
}

// applyMinimalLogs creates one logs data stream and one document on the remote.
func (node *Node) applyMinimalLogs(ctx context.Context) error {
	const dataStream = "logs-degradation-default"

	createError := node.requestJSON(
		ctx,
		http.MethodPut,
		"/_data_stream/"+dataStream,
		nil,
		nil,
	)
	if createError != nil {
		return fmt.Errorf("create logs data stream: %w", createError)
	}

	document := map[string]any{
		"@timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message":    "degradation runner fixture",
		"service": map[string]any{
			"name": "checkout",
		},
	}

	indexError := node.requestJSON(
		ctx,
		http.MethodPost,
		"/"+dataStream+"/_doc?op_type=create&refresh=true",
		document,
		nil,
	)
	if indexError != nil {
		return fmt.Errorf("index logs fixture: %w", indexError)
	}

	return nil
}

// applyMinimalLookup creates one coordinator lookup index and one enrichment document.
func (node *Node) applyMinimalLookup(ctx context.Context) error {
	const indexName = "degradation-lookup"

	indexDefinition := map[string]any{
		"settings": map[string]any{
			"index.mode": "lookup",
		},
		"mappings": map[string]any{
			"properties": map[string]any{
				"customer_id": map[string]string{
					"type": "keyword",
				},
				"customer_name": map[string]string{
					"type": "keyword",
				},
			},
		},
	}

	createError := node.requestJSON(
		ctx,
		http.MethodPut,
		"/"+indexName,
		indexDefinition,
		nil,
	)
	if createError != nil {
		return fmt.Errorf("create lookup index: %w", createError)
	}

	document := map[string]string{
		"customer_id":   "customer-1",
		"customer_name": "Mario Rossi",
	}

	indexError := node.requestJSON(
		ctx,
		http.MethodPut,
		"/"+indexName+"/_doc/1?refresh=true",
		document,
		nil,
	)
	if indexError != nil {
		return fmt.Errorf("index lookup fixture: %w", indexError)
	}

	return nil
}
