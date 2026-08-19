package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ApplyMinimalLogs creates a logs data stream and one representative document.
func (node *Node) ApplyMinimalLogs(ctx context.Context, dataStream string) error {
	if createError := node.requestJSON(ctx, http.MethodPut, "/_data_stream/"+dataStream, nil, nil); createError != nil {
		return fmt.Errorf("create logs data stream: %w", createError)
	}

	document := map[string]any{
		"@timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message":    "degradation runner fixture",
		"service":    map[string]any{"name": "checkout"},
	}
	if indexError := node.requestJSON(ctx, http.MethodPost, "/"+dataStream+"/_doc?op_type=create&refresh=true", document, nil); indexError != nil {
		return fmt.Errorf("index logs fixture: %w", indexError)
	}

	return nil
}

// ApplyMinimalLookup creates a lookup index and one enrichment document.
func (node *Node) ApplyMinimalLookup(ctx context.Context, indexName string) error {
	indexDefinition := map[string]any{
		"settings": map[string]any{"index.mode": "lookup"},
		"mappings": map[string]any{
			"properties": map[string]any{
				"customer_id":   map[string]string{"type": "keyword"},
				"customer_name": map[string]string{"type": "keyword"},
			},
		},
	}
	if createError := node.requestJSON(ctx, http.MethodPut, "/"+indexName, indexDefinition, nil); createError != nil {
		return fmt.Errorf("create lookup index: %w", createError)
	}

	document := map[string]string{"customer_id": "customer-1", "customer_name": "Mario Rossi"}
	if indexError := node.requestJSON(ctx, http.MethodPut, "/"+indexName+"/_doc/1?refresh=true", document, nil); indexError != nil {
		return fmt.Errorf("index lookup fixture: %w", indexError)
	}

	return nil
}
