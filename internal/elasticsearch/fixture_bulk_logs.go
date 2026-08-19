package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const bulkLogsTemplatePriority = 500

// ApplyBulkLogs creates mapping-heavy data streams without indexing documents.
func (node *Node) ApplyBulkLogs(
	ctx context.Context,
	namePrefix string,
	dataStreams int,
	fieldsPerStream int,
	reportProgress func(string),
) error {
	if templateError := node.requestJSON(ctx, http.MethodPut, "/_index_template/"+namePrefix, bulkLogsTemplate(namePrefix), nil); templateError != nil {
		return fmt.Errorf("create bulk logs index template: %w", templateError)
	}

	numberWidth := len(strconv.Itoa(dataStreams))
	reportedAt := time.Now()
	for streamNumber := 1; streamNumber <= dataStreams; streamNumber++ {
		dataStream := fmt.Sprintf("%s-%0*d", namePrefix, numberWidth, streamNumber)
		if createError := node.requestJSON(ctx, http.MethodPut, "/_data_stream/"+dataStream, nil, nil); createError != nil {
			return fmt.Errorf("create bulk logs data stream %s: %w", dataStream, createError)
		}
		if mappingError := node.requestJSON(ctx, http.MethodPut, "/"+dataStream+"/_mapping", bulkLogsMapping(streamNumber, numberWidth, fieldsPerStream), nil); mappingError != nil {
			return fmt.Errorf("map bulk logs data stream %s: %w", dataStream, mappingError)
		}
		if time.Since(reportedAt) >= progressInterval {
			reportProgress(fmt.Sprintf("Created %d of %d bulk logs data streams", streamNumber, dataStreams))
			reportedAt = time.Now()
		}
	}

	return nil
}

func bulkLogsMapping(streamNumber int, numberWidth int, fieldsPerStream int) map[string]any {
	properties := make(map[string]any, fieldsPerStream)
	for fieldNumber := 1; fieldNumber <= fieldsPerStream; fieldNumber++ {
		fieldName := fmt.Sprintf("ds%0*d_field_%04d", numberWidth, streamNumber, fieldNumber)
		properties[fieldName] = map[string]string{"type": "keyword"}
	}

	return map[string]any{"properties": properties}
}

func bulkLogsTemplate(namePrefix string) map[string]any {
	return map[string]any{
		"index_patterns": []string{namePrefix + "-*"},
		"data_stream":    map[string]any{},
		"priority":       bulkLogsTemplatePriority,
		"template": map[string]any{
			"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
			"mappings": map[string]any{
				"properties": map[string]any{"@timestamp": map[string]string{"type": "date"}},
			},
		},
	}
}
