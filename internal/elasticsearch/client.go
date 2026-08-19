package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// requestJSON handles authenticated JSON requests used during scenario setup.
func (node *Node) requestJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	result any,
) error {
	if body == nil {
		return node.request(ctx, method, path, "", nil, result)
	}

	encodedBody, encodeError := json.Marshal(body)
	if encodeError != nil {
		return fmt.Errorf("encode Elasticsearch request: %w", encodeError)
	}

	return node.request(ctx, method, path, "application/json", encodedBody, result)
}

// requestNDJSON sends a newline-delimited body, which is what the bulk API reads.
func (node *Node) requestNDJSON(ctx context.Context, path string, body []byte, result any) error {
	return node.request(ctx, http.MethodPost, path, "application/x-ndjson", body, result)
}

// request carries the parts every Elasticsearch call shares: credentials, status
// handling, decoding, and draining bodies so connections remain reusable.
func (node *Node) request(
	ctx context.Context,
	method string,
	path string,
	contentType string,
	body []byte,
	result any,
) error {
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}

	request, requestError := http.NewRequestWithContext(ctx, method, node.url+path, requestBody)
	if requestError != nil {
		return fmt.Errorf("create Elasticsearch request: %w", requestError)
	}

	request.SetBasicAuth(elasticsearchUsername, elasticsearchPassword)
	if contentType != "" {
		request.Header.Set("content-type", contentType)
	}

	response, responseError := node.setupHTTPClient().Do(request)
	if responseError != nil {
		return responseError
	}
	defer response.Body.Close()

	requestFailed := response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices
	if requestFailed {
		responseBody, readError := io.ReadAll(io.LimitReader(response.Body, errorBodyLimitBytes))
		drainError := drainResponseBody(response.Body)

		return errors.Join(
			fmt.Errorf("Elasticsearch returned %s: %s", response.Status, responseBody),
			wrapResponseReadError(readError),
			drainError,
		)
	}

	if result == nil {
		return drainResponseBody(response.Body)
	}

	decodeError := json.NewDecoder(response.Body).Decode(result)
	drainError := drainResponseBody(response.Body)
	if decodeError != nil {
		decodeError = fmt.Errorf("decode Elasticsearch response: %w", decodeError)
	}

	return errors.Join(decodeError, drainError)
}

func (node *Node) setupHTTPClient() *http.Client {
	if node.setupClient != nil {
		return node.setupClient
	}

	return defaultSetupClient
}

func drainResponseBody(body io.Reader) error {
	if _, drainError := io.Copy(io.Discard, body); drainError != nil {
		return fmt.Errorf("drain Elasticsearch response: %w", drainError)
	}

	return nil
}

func wrapResponseReadError(readError error) error {
	if readError == nil {
		return nil
	}

	return fmt.Errorf("read Elasticsearch error response: %w", readError)
}
