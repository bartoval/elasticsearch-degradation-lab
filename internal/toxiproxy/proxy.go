package toxiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	proxiesPath         = "/proxies"
	proxyRequestTimeout = 5 * time.Second

	// errorBodyLimitBytes keeps a failed response quotable without admitting an
	// unexpected body into the error message whole.
	errorBodyLimitBytes = 4 * 1024
)

type proxyDefinition struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Enabled  bool   `json:"enabled"`
}

type latencyDefinition struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Stream     string            `json:"stream"`
	Toxicity   int               `json:"toxicity"`
	Attributes latencyAttributes `json:"attributes"`
}

type latencyAttributes struct {
	Latency int `json:"latency"`
	Jitter  int `json:"jitter"`
}

type bandwidthDefinition struct {
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Stream     string              `json:"stream"`
	Toxicity   int                 `json:"toxicity"`
	Attributes bandwidthAttributes `json:"attributes"`
}

type bandwidthAttributes struct {
	Rate int `json:"rate"`
}

// CreateProxy places Toxiproxy between the coordinator and one remote transport port.
func CreateProxy(
	ctx context.Context,
	name string,
	listenPort int,
	upstreamPort int,
) error {
	definition := proxyDefinition{
		Name:     name,
		Listen:   localHost + ":" + strconv.Itoa(listenPort),
		Upstream: localHost + ":" + strconv.Itoa(upstreamPort),
		Enabled:  true,
	}

	return postJSON(ctx, proxiesPath, definition, "create Toxiproxy proxy")
}

// ApplyLatency delays traffic returning from the selected remote to the coordinator.
func ApplyLatency(
	ctx context.Context,
	proxyName string,
	faultName string,
	latencyMilliseconds int,
	jitterMilliseconds int,
) error {
	definition := latencyDefinition{
		Name:     faultName,
		Type:     "latency",
		Stream:   "downstream",
		Toxicity: 1,
		Attributes: latencyAttributes{
			Latency: latencyMilliseconds,
			Jitter:  jitterMilliseconds,
		},
	}

	proxyPath := proxiesPath + "/" + url.PathEscape(proxyName) + "/toxics"

	return postJSON(ctx, proxyPath, definition, "apply Toxiproxy latency")
}

// ApplyBandwidth caps the rate at which a remote's responses reach the coordinator.
// Unlike latency, which costs the same whatever the answer weighs, this makes the
// delay proportional to the bytes, so the time a request takes measures how much
// metadata the topology actually produced.
func ApplyBandwidth(
	ctx context.Context,
	proxyName string,
	faultName string,
	rateKilobytesPerSecond int,
) error {
	definition := bandwidthDefinition{
		Name:     faultName,
		Type:     "bandwidth",
		Stream:   "downstream",
		Toxicity: 1,
		Attributes: bandwidthAttributes{
			Rate: rateKilobytesPerSecond,
		},
	}

	proxyPath := proxiesPath + "/" + url.PathEscape(proxyName) + "/toxics"

	return postJSON(ctx, proxyPath, definition, "apply Toxiproxy bandwidth")
}

// postJSON performs the small subset of the Toxiproxy API needed by the MVP.
func postJSON(ctx context.Context, path string, body any, operation string) error {
	requestBody, encodeError := json.Marshal(body)
	if encodeError != nil {
		return fmt.Errorf("%s: encode request: %w", operation, encodeError)
	}

	requestContext, cancelRequest := context.WithTimeout(ctx, proxyRequestTimeout)
	defer cancelRequest()

	request, requestError := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		APIURL+path,
		bytes.NewReader(requestBody),
	)
	if requestError != nil {
		return fmt.Errorf("%s: create request: %w", operation, requestError)
	}
	request.Header.Set("content-type", "application/json")

	response, responseError := http.DefaultClient.Do(request)
	if responseError != nil {
		return fmt.Errorf("%s: %w", operation, responseError)
	}
	defer response.Body.Close()

	requestFailed := response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices
	if requestFailed {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyLimitBytes))

		return fmt.Errorf(
			"%s: %s: %s",
			operation,
			response.Status,
			responseBody,
		)
	}

	return nil
}
