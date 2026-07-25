package toxiproxy

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

const proxyRequestTimeout = 5 * time.Second

type proxyDefinition struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Enabled  bool   `json:"enabled"`
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

	requestBody, encodeError := json.Marshal(definition)
	if encodeError != nil {
		return fmt.Errorf("encode Toxiproxy proxy: %w", encodeError)
	}

	requestContext, cancelRequest := context.WithTimeout(ctx, proxyRequestTimeout)
	defer cancelRequest()

	request, requestError := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		APIURL+"/proxies",
		bytes.NewReader(requestBody),
	)
	if requestError != nil {
		return fmt.Errorf("create Toxiproxy request: %w", requestError)
	}
	request.Header.Set("content-type", "application/json")

	response, responseError := http.DefaultClient.Do(request)
	if responseError != nil {
		return fmt.Errorf("create Toxiproxy proxy: %w", responseError)
	}
	defer response.Body.Close()

	requestFailed := response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices
	if requestFailed {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))

		return fmt.Errorf(
			"create Toxiproxy proxy: %s: %s",
			response.Status,
			responseBody,
		)
	}

	return nil
}
