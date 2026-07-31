package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const remoteConnectionTimeout = 2 * time.Minute

type remoteInfo struct {
	Connected bool `json:"connected"`
}

// ConfigureRemoteAliases connects logical CCS aliases to one physical remote through Toxiproxy.
func (node *Node) ConfigureRemoteAliases(
	ctx context.Context,
	aliases []string,
	proxyPort int,
	skipUnavailable bool,
) error {
	settings := make(map[string]any, len(aliases)*4)

	for _, alias := range aliases {
		prefix := "cluster.remote." + alias
		settings[prefix+".mode"] = "proxy"
		settings[prefix+".proxy_address"] = localHost + ":" + strconv.Itoa(proxyPort)
		settings[prefix+".proxy_socket_connections"] = 1
		settings[prefix+".skip_unavailable"] = skipUnavailable
	}

	requestBody := map[string]any{
		"persistent": settings,
	}

	return node.requestJSON(
		ctx,
		http.MethodPut,
		"/_cluster/settings",
		requestBody,
		nil,
	)
}

// WaitForRemoteAliases reports progress until every logical CCS alias is connected.
func (node *Node) WaitForRemoteAliases(
	ctx context.Context,
	aliases []string,
	reportProgress func(string),
) error {
	timeout := time.NewTimer(remoteConnectionTimeout)
	defer timeout.Stop()

	checkTicker := time.NewTicker(readinessInterval)
	defer checkTicker.Stop()

	progressTicker := time.NewTicker(progressInterval)
	defer progressTicker.Stop()

	startedAt := time.Now()
	var lastError error

	for {
		connected, connectionError := node.remoteAliasesAreConnected(ctx, aliases)
		if connected {
			reportProgress(fmt.Sprintf("%d remote cluster aliases are connected", len(aliases)))

			return nil
		}

		lastError = connectionError

		select {
		case <-ctx.Done():
			return ctx.Err()
		case processError := <-node.done:
			return fmt.Errorf(
				"Elasticsearch %s exited while waiting for remote cluster aliases: %v",
				node.id,
				processError,
			)
		case <-timeout.C:
			return fmt.Errorf(
				"timed out waiting for remote cluster aliases: %v",
				lastError,
			)
		case <-progressTicker.C:
			reportProgress(
				fmt.Sprintf(
					"Waiting for %d remote cluster aliases (%s elapsed). Last error: %v",
					len(aliases),
					time.Since(startedAt).Round(time.Second),
					lastError,
				),
			)
		case <-checkTicker.C:
		}
	}
}

// remoteAliasesAreConnected checks all aliases with one remote-cluster status request.
func (node *Node) remoteAliasesAreConnected(
	ctx context.Context,
	aliases []string,
) (bool, error) {
	remotes := map[string]remoteInfo{}
	requestError := node.requestJSON(
		ctx,
		http.MethodGet,
		"/_remote/info",
		nil,
		&remotes,
	)
	if requestError != nil {
		return false, requestError
	}

	for _, alias := range aliases {
		remote, exists := remotes[alias]
		if !exists {
			return false, fmt.Errorf("remote cluster %s is not present in _remote/info", alias)
		}

		if !remote.Connected {
			return false, fmt.Errorf("remote cluster %s is not connected", alias)
		}
	}

	return true, nil
}

// requestJSON handles authenticated JSON requests used during scenario setup.
func (node *Node) requestJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	result any,
) error {
	var requestBody io.Reader

	if body != nil {
		encodedBody, encodeError := json.Marshal(body)
		if encodeError != nil {
			return fmt.Errorf("encode Elasticsearch request: %w", encodeError)
		}

		requestBody = bytes.NewReader(encodedBody)
	}

	request, requestError := http.NewRequestWithContext(
		ctx,
		method,
		node.url+path,
		requestBody,
	)
	if requestError != nil {
		return fmt.Errorf("create Elasticsearch request: %w", requestError)
	}

	request.SetBasicAuth(elasticsearchUsername, elasticsearchPassword)
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}

	response, responseError := setupHTTPClient.Do(request)
	if responseError != nil {
		return responseError
	}
	defer response.Body.Close()

	requestFailed := response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices
	if requestFailed {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))

		return fmt.Errorf("Elasticsearch returned %s: %s", response.Status, responseBody)
	}

	if result == nil {
		return nil
	}

	if decodeError := json.NewDecoder(response.Body).Decode(result); decodeError != nil {
		return fmt.Errorf("decode Elasticsearch response: %w", decodeError)
	}

	return nil
}
