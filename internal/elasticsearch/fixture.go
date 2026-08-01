package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ApplyMinimalLogs creates a logs data stream and one representative document.
func (node *Node) ApplyMinimalLogs(ctx context.Context, dataStream string) error {
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

// bulkLogsTemplatePriority outranks Elasticsearch's built-in logs template, which
// is priority 100 and claims logs-*-* for data streams only.
const bulkLogsTemplatePriority = 500

// ApplyBulkLogs creates many logs data streams so that index resolution and field
// capabilities become expensive. Documents are deliberately not indexed: both
// _resolve/index and _field_caps read mappings, so the mappings alone reproduce
// the cost while keeping the fixture fast to create and to reset.
//
// Data streams rather than plain indices, because that is what a real logs
// deployment resolves, and because the built-in logs template already reserves
// this name shape for them.
//
// Every data stream gets its own field names. _field_caps merges by field name,
// so streams sharing one schema collapse into a single small response no matter
// how many of them exist, which is not what a real deployment looks like: there
// each integration contributes its own fields and the union is what Kibana has to
// hold. Distinct names are therefore what makes the field list, and the browser
// memory it occupies, grow with the fixture.
func (node *Node) ApplyBulkLogs(
	ctx context.Context,
	namePrefix string,
	dataStreams int,
	fieldsPerStream int,
	reportProgress func(string),
) error {
	templateError := node.requestJSON(
		ctx,
		http.MethodPut,
		"/_index_template/"+namePrefix,
		bulkLogsTemplate(namePrefix),
		nil,
	)
	if templateError != nil {
		return fmt.Errorf("create bulk logs index template: %w", templateError)
	}

	numberWidth := len(strconv.Itoa(dataStreams))
	reportedAt := time.Now()

	for streamNumber := 1; streamNumber <= dataStreams; streamNumber++ {
		dataStream := fmt.Sprintf("%s-%0*d", namePrefix, numberWidth, streamNumber)

		createError := node.requestJSON(
			ctx,
			http.MethodPut,
			"/_data_stream/"+dataStream,
			nil,
			nil,
		)
		if createError != nil {
			return fmt.Errorf("create bulk logs data stream %s: %w", dataStream, createError)
		}

		// The template only carries @timestamp, so the fields unique to this
		// stream are added afterwards, onto its backing indices.
		mappingError := node.requestJSON(
			ctx,
			http.MethodPut,
			"/"+dataStream+"/_mapping",
			bulkLogsMapping(streamNumber, numberWidth, fieldsPerStream),
			nil,
		)
		if mappingError != nil {
			return fmt.Errorf("map bulk logs data stream %s: %w", dataStream, mappingError)
		}

		// Each data stream is a cluster state update, so a large fixture takes
		// long enough that silence would look like a hang.
		if time.Since(reportedAt) >= progressInterval {
			reportProgress(
				fmt.Sprintf("Created %d of %d bulk logs data streams", streamNumber, dataStreams),
			)
			reportedAt = time.Now()
		}
	}

	return nil
}

// bulkLogsMapping names every field after its own data stream, so that the union
// _field_caps returns grows with the fixture instead of collapsing to one schema.
func bulkLogsMapping(streamNumber int, numberWidth int, fieldsPerStream int) map[string]any {
	properties := make(map[string]any, fieldsPerStream)

	for fieldNumber := 1; fieldNumber <= fieldsPerStream; fieldNumber++ {
		fieldName := fmt.Sprintf(
			"ds%0*d_field_%04d",
			numberWidth,
			streamNumber,
			fieldNumber,
		)
		properties[fieldName] = map[string]string{"type": "keyword"}
	}

	return map[string]any{"properties": properties}
}

// bulkLogsTemplate claims the generated names for data streams. It carries only
// @timestamp, because each stream is mapped separately with its own fields.
func bulkLogsTemplate(namePrefix string) map[string]any {
	return map[string]any{
		"index_patterns": []string{namePrefix + "-*"},
		"data_stream":    map[string]any{},
		"priority":       bulkLogsTemplatePriority,
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":   1,
				"number_of_replicas": 0,
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					"@timestamp": map[string]string{"type": "date"},
				},
			},
		},
	}
}

// ApplyMinimalLookup creates a lookup index and one enrichment document.
func (node *Node) ApplyMinimalLookup(ctx context.Context, indexName string) error {
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
