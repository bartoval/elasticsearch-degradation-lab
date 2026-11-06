package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestApplyMultipliedDocumentsPreservesMappingAndCopiesOnlySource(testContext *testing.T) {
	var requests []string
	var prefixes []string
	transport := func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		responseBody := `{}`

		switch request.URL.Path {
		case "/logs-seed":
			responseBody = `{"logs-seed":{"mappings":{"properties":{"message":{"type":"text","analyzer":"fixture"}}},"settings":{"index":{"uuid":"discard","analysis":{"analyzer":{"fixture":{"type":"standard"}}},"mapping":{"total_fields":{"limit":"2000"}}}}}}`
		case "/logs-seed/_count":
			responseBody = `{"count":2,"_shards":{"failed":0}}`
		case "/logs-expanded":
			var definition struct {
				Mappings json.RawMessage `json:"mappings"`
				Settings map[string]any  `json:"settings"`
			}
			if decodeError := json.NewDecoder(request.Body).Decode(&definition); decodeError != nil {
				testContext.Fatalf("decode destination: %v", decodeError)
			}

			if !strings.Contains(string(definition.Mappings), `"analyzer":"fixture"`) ||
				definition.Settings["analysis"] == nil || definition.Settings["mapping"] == nil ||
				definition.Settings["uuid"] != nil {
				testContext.Fatalf("unexpected destination definition: %+v", definition)
			}
		case "/_reindex":
			var reindex struct {
				Source map[string]string `json:"source"`
				Dest   map[string]string `json:"dest"`
				Script struct {
					Params map[string]string `json:"params"`
				} `json:"script"`
			}
			if decodeError := json.NewDecoder(request.Body).Decode(&reindex); decodeError != nil {
				testContext.Fatalf("decode reindex: %v", decodeError)
			}

			if reindex.Source["index"] != "logs-seed" || reindex.Dest["index"] != "logs-expanded" || reindex.Dest["op_type"] != "create" {
				testContext.Fatalf("unexpected reindex request: %+v", reindex)
			}

			prefixes = append(prefixes, reindex.Script.Params["prefix"])
			responseBody = `{"task":"node:1"}`
		case "/_tasks/node:1":
			responseBody = `{"completed":true,"response":{"created":2,"failures":[],"timed_out":false}}`
		}

		return multiplicationResponse(responseBody), nil
	}
	node := &Node{url: "http://elasticsearch.invalid", setupClient: &http.Client{Transport: roundTripFunc(transport)}}
	copyError := node.ApplyMultipliedDocuments(context.Background(), "logs-expanded", DocumentMultiplication{Source: "logs-seed", Copies: 2, MaxDocuments: 10}, func(string) {})
	if copyError != nil {
		testContext.Fatalf("multiply documents: %v", copyError)
	}

	if !reflect.DeepEqual(prefixes, []string{"copy-1:", "copy-2:"}) {
		testContext.Fatalf("copy prefixes = %v", prefixes)
	}

	expectedRequests := []string{
		"GET /logs-seed", "POST /logs-seed/_refresh", "GET /logs-seed/_count", "PUT /logs-expanded",
		"POST /_reindex?wait_for_completion=false", "GET /_tasks/node:1",
		"POST /_reindex?wait_for_completion=false", "GET /_tasks/node:1", "POST /logs-expanded/_refresh",
	}
	if !reflect.DeepEqual(requests, expectedRequests) {
		testContext.Fatalf("requests = %v, want %v", requests, expectedRequests)
	}
}

func TestApplyMultipliedDocumentsRejectsOversizedDestinationBeforeCreation(testContext *testing.T) {
	var requests []string
	transport := func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		responses := map[string]string{
			"/logs-seed":          `{"logs-seed":{"mappings":{},"settings":{"index":{}}}}`,
			"/logs-seed/_refresh": `{}`,
			"/logs-seed/_count":   `{"count":6}`,
		}

		return multiplicationResponse(responses[request.URL.Path]), nil
	}
	node := &Node{url: "http://elasticsearch.invalid", setupClient: &http.Client{Transport: roundTripFunc(transport)}}
	copyError := node.ApplyMultipliedDocuments(context.Background(), "logs-expanded", DocumentMultiplication{Source: "logs-seed", Copies: 2, MaxDocuments: 10}, func(string) {})
	if copyError == nil || !strings.Contains(copyError.Error(), "exceeds the 10 document limit") {
		testContext.Fatalf("expected document limit error, got %v", copyError)
	}

	if len(requests) != 3 {
		testContext.Fatalf("destination was changed after preflight failure: %v", requests)
	}
}

func TestDocumentCopyRejectsPartialAndFailedResults(testContext *testing.T) {
	testCases := []struct {
		name     string
		response string
		expected string
	}{
		{name: "task error", response: `{"completed":true,"error":{"reason":"broken source"}}`, expected: "broken source"},
		{name: "item error", response: `{"completed":true,"response":{"created":1,"failures":[{"cause":{"reason":"bad mapping"}}]}}`, expected: "bad mapping"},
		{name: "timeout", response: `{"completed":true,"response":{"timed_out":true}}`, expected: "timed out"},
		{name: "partial copy", response: `{"completed":true,"response":{"created":1}}`, expected: "expected 2"},
	}

	for _, testCase := range testCases {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			transport := func(request *http.Request) (*http.Response, error) {
				return multiplicationResponse(testCase.response), nil
			}
			node := &Node{url: "http://elasticsearch.invalid", setupClient: &http.Client{Transport: roundTripFunc(transport)}}
			copyError := node.waitForDocumentCopy(context.Background(), "node:1", 2, func(string) {})
			if copyError == nil || !strings.Contains(copyError.Error(), testCase.expected) {
				testContext.Fatalf("expected %q error, got %v", testCase.expected, copyError)
			}
		})
	}
}

func TestDocumentCopyCancelsTaskWhenContextEnds(testContext *testing.T) {
	ctx, cancelCopy := context.WithCancel(context.Background())
	defer cancelCopy()

	var requests []string
	transport := func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		cancelCopy()

		return multiplicationResponse(`{"completed":false}`), nil
	}
	node := &Node{url: "http://elasticsearch.invalid", setupClient: &http.Client{Transport: roundTripFunc(transport)}}
	copyError := node.waitForDocumentCopy(ctx, "node:1", 2, func(string) {})
	if !errors.Is(copyError, context.Canceled) {
		testContext.Fatalf("expected cancellation, got %v", copyError)
	}

	expectedRequests := []string{"GET /_tasks/node:1", "POST /_tasks/node:1/_cancel"}
	if !reflect.DeepEqual(requests, expectedRequests) {
		testContext.Fatalf("requests = %v, want %v", requests, expectedRequests)
	}
}

func multiplicationResponse(contents string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(contents)),
		Header:     make(http.Header),
	}
}
