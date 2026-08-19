package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// bulkRequestTargetBytes sizes one _bulk request by weight rather than by document
// count, so that a fixture of wide rows does not build a request many times larger
// than one of narrow rows.
const bulkRequestTargetBytes = 8 * 1024 * 1024

// maxBulkFailureDetails keeps a broken large fixture actionable without printing
// hundreds of thousands of near-identical item failures.
const maxBulkFailureDetails = 5

// defaultMessageBytes is roughly the length of an ordinary log line, used when a
// scenario does not say how heavy a row should be.
const defaultMessageBytes = 48

// DocumentShape describes what one generated document weighs. Row weight and row
// count are separate knobs because they answer different questions: whether a client
// struggles with the number of cells it has to lay out, or with the volume of text
// it has to receive and hold.
type DocumentShape struct {
	Count             int
	FieldsPerDocument int
	MessageBytes      int
	TimeRange         time.Duration
}

type bulkResponse struct {
	Errors bool                        `json:"errors"`
	Items  []map[string]bulkItemResult `json:"items"`
}

type bulkItemResult struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Status int             `json:"status"`
	Error  json.RawMessage `json:"error"`
}

// withDefaults fills the sizes a scenario left unset, so that the preset stays
// usable when only a document count is given.
func (shape DocumentShape) withDefaults() DocumentShape {
	if shape.MessageBytes <= 0 {
		shape.MessageBytes = defaultMessageBytes
	}

	return shape
}

// estimatedDocumentBytes approximates one rendered document, to choose a batch size
// before any of them exists.
func (shape DocumentShape) estimatedDocumentBytes() int {
	const fixedFieldBytes = 160
	const generatedFieldBytes = 40

	return fixedFieldBytes + shape.MessageBytes + shape.FieldsPerDocument*generatedFieldBytes
}

// batchSize keeps one request near the target weight, and never below one document.
func (shape DocumentShape) batchSize() int {
	return max(1, bulkRequestTargetBytes/shape.estimatedDocumentBytes())
}

// ApplyBulkDocuments indexes many real documents into one index, for scenarios whose
// subject is the size of a result rather than the shape of the metadata. This is the
// preset to reach for when a query has to actually return rows: the bulk_logs preset
// deliberately indexes nothing, because index resolution and field capabilities read
// mappings alone.
//
// Timestamps use either the explicit TimeRange or the historical one-second spacing
// backwards from fixture creation, so a client time picker selects a predictable
// number of rows.
func (node *Node) ApplyBulkDocuments(
	ctx context.Context,
	indexName string,
	shape DocumentShape,
	reportProgress func(string),
) error {
	shape = shape.withDefaults()

	createError := node.requestJSON(
		ctx,
		http.MethodPut,
		"/"+indexName,
		bulkDocumentIndex(shape),
		nil,
	)
	if createError != nil {
		return fmt.Errorf("create documents index: %w", createError)
	}

	batchSize := shape.batchSize()
	reportedAt := time.Now()
	indexedAt := time.Now().UTC()

	for indexed := 0; indexed < shape.Count; indexed += batchSize {
		batch := min(batchSize, shape.Count-indexed)

		var response bulkResponse
		bulkError := node.requestNDJSON(
			ctx,
			"/"+indexName+"/_bulk",
			bulkDocumentBody(indexed, batch, indexedAt, shape),
			&response,
		)
		if bulkError != nil {
			return fmt.Errorf("index documents %d-%d: %w", indexed, indexed+batch, bulkError)
		}
		if itemError := response.itemError(); itemError != nil {
			return fmt.Errorf("index documents %d-%d: %w", indexed, indexed+batch, itemError)
		}

		if time.Since(reportedAt) >= progressInterval {
			reportProgress(
				fmt.Sprintf("Indexed %d of %d documents", indexed+batch, shape.Count),
			)
			reportedAt = time.Now()
		}
	}

	// Without a refresh the documents exist but no query returns them, which would
	// look like a fixture that silently did nothing.
	refreshError := node.requestJSON(ctx, http.MethodPost, "/"+indexName+"/_refresh", nil, nil)
	if refreshError != nil {
		return fmt.Errorf("refresh documents index: %w", refreshError)
	}

	return nil
}

// bulkDocumentIndex maps the fields the generated documents carry: a fixed core, and
// the extra columns the scenario asked for.
func bulkDocumentIndex(shape DocumentShape) map[string]any {
	properties := map[string]any{
		"@timestamp": map[string]string{"type": "date"},
		"message":    map[string]string{"type": "text"},
		"sequence":   map[string]string{"type": "long"},
		"service":    map[string]string{"type": "keyword"},
		"level":      map[string]string{"type": "keyword"},
	}

	for fieldNumber := 1; fieldNumber <= shape.FieldsPerDocument; fieldNumber++ {
		properties[generatedFieldName(fieldNumber)] = map[string]string{"type": "keyword"}
	}

	return map[string]any{
		"settings": map[string]any{
			"number_of_shards":   1,
			"number_of_replicas": 0,
		},
		"mappings": map[string]any{"properties": properties},
	}
}

// generatedFieldName names the extra columns predictably, so that a query can select
// or drop them by pattern to separate cell count from text volume.
func generatedFieldName(fieldNumber int) string {
	return fmt.Sprintf("field_%04d", fieldNumber)
}

// bulkDocumentBody renders one batch as the action/document pairs _bulk expects.
func bulkDocumentBody(
	firstSequence int,
	count int,
	indexedAt time.Time,
	shape DocumentShape,
) []byte {
	levels := [...]string{"info", "warn", "error"}
	services := [...]string{"checkout", "search", "billing", "auth"}

	var body strings.Builder
	body.Grow(count * shape.estimatedDocumentBytes())

	for offset := range count {
		sequence := firstSequence + offset

		body.WriteString("{\"index\":{}}\n")
		body.WriteString(fmt.Sprintf(
			`{"@timestamp":%q,"message":%q,"sequence":%d,"service":%q,"level":%q`,
			documentTimestamp(indexedAt, sequence, shape).Format(time.RFC3339Nano),
			messageOfLength(sequence, shape.MessageBytes),
			sequence,
			services[sequence%len(services)],
			levels[sequence%len(levels)],
		))

		for fieldNumber := 1; fieldNumber <= shape.FieldsPerDocument; fieldNumber++ {
			body.WriteString(fmt.Sprintf(
				`,%q:"value-%d-%d"`,
				generatedFieldName(fieldNumber),
				sequence,
				fieldNumber,
			))
		}

		body.WriteString("}\n")
	}

	return []byte(body.String())
}

// documentTimestamp keeps the historical one-second spacing unless a scenario
// asks for an explicit range. Explicit ranges include both endpoints so a count of
// 500k over 24h has the same intent as makelogs --days 1/0.
func documentTimestamp(indexedAt time.Time, sequence int, shape DocumentShape) time.Time {
	if shape.TimeRange <= 0 {
		return indexedAt.Add(-time.Duration(sequence) * time.Second)
	}

	if shape.Count <= 1 {
		return indexedAt
	}

	denominator := time.Duration(shape.Count - 1)
	sequenceDuration := time.Duration(sequence)
	wholeStep := shape.TimeRange / denominator
	remainder := shape.TimeRange % denominator
	elapsed := wholeStep*sequenceDuration + remainder*sequenceDuration/denominator

	return indexedAt.Add(-elapsed)
}

func (response bulkResponse) itemError() error {
	failureCount := 0
	failureDetails := make([]string, 0, maxBulkFailureDetails)
	operations := [...]string{"index", "create", "update", "delete"}

	for _, item := range response.Items {
		for _, operation := range operations {
			result, exists := item[operation]
			if !exists || (result.Status < http.StatusMultipleChoices && !hasBulkItemError(result.Error)) {
				continue
			}

			failureCount++
			if len(failureDetails) < maxBulkFailureDetails {
				failureDetails = append(
					failureDetails,
					formatBulkItemFailure(operation, result),
				)
			}
		}
	}

	if failureCount == 0 {
		if response.Errors {
			return fmt.Errorf("Elasticsearch _bulk reported item failures without details")
		}

		return nil
	}

	detail := strings.Join(failureDetails, "; ")
	if omitted := failureCount - len(failureDetails); omitted > 0 {
		detail += fmt.Sprintf("; and %d more", omitted)
	}

	return fmt.Errorf("Elasticsearch _bulk reported %d item failure(s): %s", failureCount, detail)
}

func hasBulkItemError(rawError json.RawMessage) bool {
	return len(rawError) > 0 && string(rawError) != "null"
}

func formatBulkItemFailure(operation string, result bulkItemResult) string {
	target := result.Index
	if result.ID != "" {
		target += "/" + result.ID
	}

	reason := bulkItemFailureReason(result.Error)
	if reason == "" {
		return fmt.Sprintf("%s %s returned status %d", operation, target, result.Status)
	}

	return fmt.Sprintf("%s %s returned status %d: %s", operation, target, result.Status, reason)
}

func bulkItemFailureReason(rawError json.RawMessage) string {
	if len(rawError) == 0 || string(rawError) == "null" {
		return ""
	}

	var structured struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if decodeError := json.Unmarshal(rawError, &structured); decodeError == nil {
		if structured.Type != "" && structured.Reason != "" {
			return structured.Type + ": " + structured.Reason
		}
		if structured.Reason != "" {
			return structured.Reason
		}
	}

	const rawErrorLimit = 512
	if len(rawError) > rawErrorLimit {
		return string(rawError[:rawErrorLimit]) + "..."
	}

	return string(rawError)
}

// messageOfLength builds a log line of about the requested size. The text repeats
// rather than being random, so that two runs of one scenario produce the same bytes
// and a measurement can be compared with an earlier one.
func messageOfLength(sequence int, messageBytes int) string {
	prefix := fmt.Sprintf("request %d completed ", sequence)
	if len(prefix) >= messageBytes {
		return prefix[:messageBytes]
	}

	const filler = "abcdefghijklmnopqrstuvwxyz0123456789 "

	var line strings.Builder
	line.Grow(messageBytes)
	line.WriteString(prefix)

	for line.Len() < messageBytes {
		remaining := messageBytes - line.Len()
		if remaining >= len(filler) {
			line.WriteString(filler)

			continue
		}

		line.WriteString(filler[:remaining])
	}

	return line.String()
}
