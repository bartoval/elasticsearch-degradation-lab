package elasticsearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type trackingBody struct {
	reader *strings.Reader
	closed bool
}

func (body *trackingBody) Read(contents []byte) (int, error) {
	return body.reader.Read(contents)
}

func (body *trackingBody) Close() error {
	body.closed = true

	return nil
}

func TestApplyBulkDocumentsRejectsItemFailure(t *testing.T) {
	t.Parallel()

	setupClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{}`
		if request.URL.Path == "/logs-test/_bulk" {
			body = `{
				"errors": true,
				"items": [{
					"index": {
						"_index": "logs-test",
						"_id": "one",
						"status": 400,
						"error": {"type": "mapper_parsing_exception", "reason": "bad field"}
					}
				}]
			}`
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	node := &Node{url: "http://elasticsearch.invalid", setupClient: setupClient}
	fixtureError := node.ApplyBulkDocuments(
		context.Background(),
		"logs-test",
		DocumentShape{Count: 1},
		func(string) {},
	)
	if fixtureError == nil {
		t.Fatal("expected an item-level bulk failure")
	}

	errorText := fixtureError.Error()
	for _, expected := range []string{
		"Elasticsearch _bulk reported 1 item failure",
		"logs-test/one",
		"mapper_parsing_exception: bad field",
	} {
		if !strings.Contains(errorText, expected) {
			t.Fatalf("expected error %q to contain %q", errorText, expected)
		}
	}
}

func TestRequestDrainsAndClosesUnusedResponse(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader("response contents")}
	setupClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       body,
			Header:     make(http.Header),
		}, nil
	})}

	node := &Node{url: "http://elasticsearch.invalid", setupClient: setupClient}
	if requestError := node.request(
		context.Background(),
		http.MethodGet,
		"/_test",
		"",
		nil,
		nil,
	); requestError != nil {
		t.Fatalf("request returned an error: %v", requestError)
	}

	if body.reader.Len() != 0 {
		t.Fatalf("expected response body to be drained, %d bytes remain", body.reader.Len())
	}
	if !body.closed {
		t.Fatal("expected response body to be closed")
	}
}

func TestBulkItemErrorLimitsReportedDetails(t *testing.T) {
	t.Parallel()
	items := make([]map[string]bulkItemResult, maxBulkFailureDetails+2)
	for itemIndex := range items {
		items[itemIndex] = map[string]bulkItemResult{
			"index": {
				Index:  "logs-test",
				ID:     string(rune('a' + itemIndex)),
				Status: http.StatusBadRequest,
				Error:  []byte(`{"type":"test_error","reason":"broken"}`),
			},
		}
	}

	itemError := (bulkResponse{Errors: true, Items: items}).itemError()
	if itemError == nil {
		t.Fatal("expected bulk item error")
	}
	if errorText := itemError.Error(); !strings.Contains(errorText, "7 item failure(s)") ||
		!strings.Contains(errorText, "and 2 more") {
		t.Fatalf("unexpected limited error details: %s", errorText)
	}
}

func TestDocumentTimestampUsesExplicitRange(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	shape := DocumentShape{Count: 5, TimeRange: 24 * time.Hour}

	if actual := documentTimestamp(anchor, 0, shape); !actual.Equal(anchor) {
		t.Fatalf("newest timestamp = %s, want %s", actual, anchor)
	}
	if actual := documentTimestamp(anchor, 2, shape); !actual.Equal(anchor.Add(-12 * time.Hour)) {
		t.Fatalf("middle timestamp = %s, want %s", actual, anchor.Add(-12*time.Hour))
	}
	if actual := documentTimestamp(anchor, 4, shape); !actual.Equal(anchor.Add(-24 * time.Hour)) {
		t.Fatalf("oldest timestamp = %s, want %s", actual, anchor.Add(-24*time.Hour))
	}
}

func TestDocumentTimestampPreservesOneSecondDefault(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	actual := documentTimestamp(anchor, 3, DocumentShape{Count: 10})
	wanted := anchor.Add(-3 * time.Second)
	if !actual.Equal(wanted) {
		t.Fatalf("timestamp = %s, want %s", actual, wanted)
	}
}
