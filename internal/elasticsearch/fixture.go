package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// bulkRequestTargetBytes sizes one _bulk request by weight rather than by document
// count, so that a fixture of wide rows does not build a request many times larger
// than one of narrow rows.
const bulkRequestTargetBytes = 8 * 1024 * 1024

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
// Timestamps walk backwards one second at a time from the moment the fixture runs,
// so a run is reproducible and a time range selects a predictable number of rows.
// A large fixture therefore reaches days into the past, and a client whose default
// time range is minutes will show nothing until it is widened.
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

		bulkError := node.requestNDJSON(
			ctx,
			"/"+indexName+"/_bulk",
			bulkDocumentBody(indexed, batch, indexedAt, shape),
			nil,
		)
		if bulkError != nil {
			return fmt.Errorf("index documents %d-%d: %w", indexed, indexed+batch, bulkError)
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
			indexedAt.Add(-time.Duration(sequence)*time.Second).Format(time.RFC3339Nano),
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
