package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const remoteConnectionTimeout = 2 * time.Minute

type remoteInfo struct {
	Connected bool `json:"connected"`
}

// RemoteAliasBinding pairs one logical CCS alias with the Toxiproxy port in front of it.
// Each alias needs its own port so a fault can degrade one simulated cluster
// without touching the others.
type RemoteAliasBinding struct {
	Alias           string
	ProxyPort       int
	SkipUnavailable bool
}

// ConfigureRemoteAliases connects logical CCS aliases to one physical remote through Toxiproxy.
// Every alias is registered in a single settings update so that a wide topology
// costs one request instead of one per alias.
func (node *Node) ConfigureRemoteAliases(
	ctx context.Context,
	bindings []RemoteAliasBinding,
) error {
	settings := make(map[string]any, len(bindings)*4)

	for _, binding := range bindings {
		prefix := "cluster.remote." + binding.Alias
		settings[prefix+".mode"] = "proxy"
		settings[prefix+".proxy_address"] = localHost + ":" + strconv.Itoa(binding.ProxyPort)
		settings[prefix+".proxy_socket_connections"] = 1
		settings[prefix+".skip_unavailable"] = binding.SkipUnavailable
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
		pending, connectionError := node.pendingRemoteAliases(ctx, aliases)

		switch {
		case connectionError != nil:
			lastError = connectionError
		case len(pending) == 0:
			reportProgress(fmt.Sprintf("%d remote cluster aliases are connected", len(aliases)))

			return nil
		default:
			// Reporting how many aliases are still missing turns a long wait over a
			// wide topology into visible progress instead of a silent stall.
			lastError = fmt.Errorf(
				"%d of %d aliases are not connected yet, first pending is %s",
				len(pending),
				len(aliases),
				pending[0],
			)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-node.child.Done():
			return fmt.Errorf(
				"Elasticsearch %s exited while waiting for remote cluster aliases: %v",
				node.id,
				node.child.Wait(),
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

// pendingRemoteAliases lists the aliases still missing or disconnected, using one
// remote-cluster status request for the whole topology.
func (node *Node) pendingRemoteAliases(
	ctx context.Context,
	aliases []string,
) ([]string, error) {
	remotes := map[string]remoteInfo{}
	requestError := node.requestJSON(
		ctx,
		http.MethodGet,
		"/_remote/info",
		nil,
		&remotes,
	)
	if requestError != nil {
		return nil, requestError
	}

	var pending []string

	for _, alias := range aliases {
		remote, exists := remotes[alias]
		if !exists || !remote.Connected {
			pending = append(pending, alias)
		}
	}

	return pending, nil
}
