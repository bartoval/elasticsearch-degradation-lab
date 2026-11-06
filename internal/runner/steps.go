package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/elasticsearch"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/toxiproxy"
)

// startToxiproxy leaves a server that is already answering its API, so that callers
// can create proxies without each of them having to wonder whether it is up yet.
func startToxiproxy(
	ctx context.Context,
	runtimeDirectory string,
	logPath string,
	reportProgress func(string),
) (*toxiproxy.Server, error) {
	binaryPath, binaryError := toxiproxy.EnsureBinary(ctx, runtimeDirectory, reportProgress)
	if binaryError != nil {
		return nil, fmt.Errorf("prepare Toxiproxy: %w", binaryError)
	}

	reportProgress("Starting Toxiproxy")

	server, startError := toxiproxy.Start(context.Background(), binaryPath, logPath)
	if startError != nil {
		return nil, fmt.Errorf("start Toxiproxy: %w", startError)
	}

	if readinessError := server.WaitUntilReady(ctx); readinessError != nil {
		if stopError := server.Stop(); stopError != nil {
			return nil, errors.Join(
				fmt.Errorf("start Toxiproxy: %v\nFull log: %s", readinessError, logPath),
				fmt.Errorf("cleanup Toxiproxy: %w", stopError),
			)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf("start Toxiproxy: %w\nFull log: %s", readinessError, logPath)
	}

	reportProgress("Toxiproxy is ready at " + toxiproxy.APIURL)

	return server, nil
}

// remoteAliasBindings pairs every generated alias with the proxy port reserved for
// it. Deriving both from one place is what keeps the proxy the runner creates and
// the address it later hands Elasticsearch from drifting apart, since a mismatch
// would surface only as an alias that never connects.
func remoteAliasBindings(remote scenario.Remote) []elasticsearch.RemoteAliasBinding {
	aliases := remote.Aliases()
	bindings := make([]elasticsearch.RemoteAliasBinding, len(aliases))

	for aliasIndex, alias := range aliases {
		bindings[aliasIndex] = elasticsearch.RemoteAliasBinding{
			Alias:           alias,
			ProxyPort:       remoteProxyBasePort + aliasIndex,
			SkipUnavailable: remote.SkipUnavailable,
		}
	}

	return bindings
}

// bindingAliases lists the proxy names, which are the alias names, for callers that
// address a proxy rather than configure one.
func bindingAliases(bindings []elasticsearch.RemoteAliasBinding) []string {
	aliases := make([]string, len(bindings))
	for bindingIndex, binding := range bindings {
		aliases[bindingIndex] = binding.Alias
	}

	return aliases
}

// createProxies puts Toxiproxy in front of everything the scenario can degrade:
// the coordinator, so that a fault can reach what Kibana asks Elasticsearch
// directly, and each alias separately, so that a wide topology can be degraded one
// simulated cluster at a time.
func createProxies(
	ctx context.Context,
	bindings []elasticsearch.RemoteAliasBinding,
	reportProgress func(string),
) error {
	reportProgress(
		fmt.Sprintf("Creating coordinator proxy on port %d, which Kibana uses", coordinatorProxyPort),
	)

	coordinatorError := toxiproxy.CreateProxy(
		ctx,
		scenario.CoordinatorTarget,
		coordinatorProxyPort,
		coordinatorHTTPPort,
	)
	if coordinatorError != nil {
		return fmt.Errorf("create coordinator proxy: %w", coordinatorError)
	}

	if len(bindings) == 0 {
		return nil
	}

	reportProgress(
		fmt.Sprintf(
			"Creating %d transport proxies on ports %d-%d",
			len(bindings),
			bindings[0].ProxyPort,
			bindings[len(bindings)-1].ProxyPort,
		),
	)

	for _, binding := range bindings {
		proxyError := toxiproxy.CreateProxy(
			ctx,
			binding.Alias,
			binding.ProxyPort,
			remoteTransportPort,
		)
		if proxyError != nil {
			return fmt.Errorf("create proxy for alias %s: %w", binding.Alias, proxyError)
		}
	}

	return nil
}

// connectRemoteAliases registers every alias on the coordinator and waits until
// Elasticsearch confirms them, so that the scenario never starts degrading a
// topology that was not connected in the first place.
func connectRemoteAliases(
	ctx context.Context,
	coordinatorNode *elasticsearch.Node,
	bindings []elasticsearch.RemoteAliasBinding,
	reportProgress func(string),
) error {
	reportProgress(
		fmt.Sprintf(
			"Configuring %d CCS aliases (%s .. %s)",
			len(bindings),
			bindings[0].Alias,
			bindings[len(bindings)-1].Alias,
		),
	)

	if configurationError := coordinatorNode.ConfigureRemoteAliases(ctx, bindings); configurationError != nil {
		return fmt.Errorf("configure remote cluster: %w", configurationError)
	}

	connectionError := coordinatorNode.WaitForRemoteAliases(
		ctx,
		bindingAliases(bindings),
		reportProgress,
	)
	if connectionError != nil {
		return fmt.Errorf("connect remote cluster: %w", connectionError)
	}

	return nil
}

// startElasticsearch gives both local clusters identical launch handling. Readiness
// is separate so the environment can register supervision before it begins.
func startElasticsearch(
	ctx context.Context,
	kibanaDirectory string,
	sessionDirectory string,
	role string,
	options elasticsearch.Options,
) (*elasticsearch.Node, error) {
	node, startError := elasticsearch.Start(
		ctx,
		kibanaDirectory,
		sessionDirectory,
		options,
	)
	if startError != nil {
		return nil, fmt.Errorf("start %s Elasticsearch: %w", role, startError)
	}

	return node, nil
}

func waitForElasticsearch(
	ctx context.Context,
	node *elasticsearch.Node,
	role string,
	reportProgress func(string),
) error {
	reportProgress(fmt.Sprintf("%s Elasticsearch log: %s", role, node.LogPath()))

	if readinessError := node.WaitUntilReady(ctx, reportProgress); readinessError != nil {
		return fmt.Errorf("wait for %s Elasticsearch: %w", role, readinessError)
	}

	return nil
}

// applyFixtures sends each data preset to the target selected by the scenario.
func applyFixtures(
	ctx context.Context,
	options Options,
	sessionDirectory string,
	coordinatorNode *elasticsearch.Node,
	remoteNode *elasticsearch.Node,
) error {
	for _, fixture := range options.Scenario.Fixtures {
		targetNode := remoteNode
		if fixture.Target == scenario.CoordinatorTarget {
			targetNode = coordinatorNode
		}

		if targetNode == nil {
			return fmt.Errorf("fixture target %q is not configured", fixture.Target)
		}

		options.ReportProgress(fixtureDescription(fixture))

		var fixtureError error

		switch fixture.Preset {
		case scenario.FixturePresetESArchive:
			fixtureError = targetNode.ApplyArchive(
				ctx,
				options.KibanaDirectory,
				fixture.Archive,
				filepath.Join(sessionDirectory, "logs", "fixture-"+fixture.Name+".log"),
			)
		case scenario.FixturePresetMultiplyDocuments:
			fixtureError = targetNode.ApplyMultipliedDocuments(
				ctx,
				fixture.Name,
				elasticsearch.DocumentMultiplication{
					Source:       fixture.Source,
					Copies:       fixture.Copies,
					MaxDocuments: scenario.MaxBulkDocuments,
				},
				options.ReportProgress,
			)
		case scenario.FixturePresetMinimalLogs:
			fixtureError = targetNode.ApplyMinimalLogs(ctx, fixture.Name)
		case scenario.FixturePresetMinimalLookup:
			fixtureError = targetNode.ApplyMinimalLookup(ctx, fixture.Name)
		case scenario.FixturePresetBulkDocuments:
			var timeRange time.Duration
			if fixture.TimeRange != "" {
				parsedTimeRange, parseError := time.ParseDuration(fixture.TimeRange)
				if parseError != nil {
					return fmt.Errorf(
						"parse fixture %s time range %q: %w",
						fixture.Name,
						fixture.TimeRange,
						parseError,
					)
				}
				timeRange = parsedTimeRange
			}

			fixtureError = targetNode.ApplyBulkDocuments(
				ctx,
				fixture.Name,
				elasticsearch.DocumentShape{
					Count:             fixture.Documents,
					FieldsPerDocument: fixture.FieldsPerDocument,
					MessageBytes:      fixture.MessageBytes,
					TimeRange:         timeRange,
				},
				options.ReportProgress,
			)
		case scenario.FixturePresetBulkLogs:
			fixtureError = targetNode.ApplyBulkLogs(
				ctx,
				fixture.Name,
				fixture.DataStreams,
				fixture.FieldsPerStream,
				options.ReportProgress,
			)
		default:
			return fmt.Errorf("fixture preset %q is not supported", fixture.Preset)
		}

		if fixtureError != nil {
			return fmt.Errorf(
				"apply fixture %s to %s: %w",
				fixture.Preset,
				fixture.Target,
				fixtureError,
			)
		}
	}

	return nil
}

// fixtureDescription states the size of a fixture up front, because a bulk preset
// takes long enough that its cost should be visible before it starts.
func fixtureDescription(fixture scenario.Fixture) string {
	switch fixture.Preset {
	case scenario.FixturePresetESArchive:
		return fmt.Sprintf(
			"Applying fixture %s to %s (archive %s)",
			fixture.Preset,
			fixture.Target,
			fixture.Archive,
		)
	case scenario.FixturePresetMultiplyDocuments:
		return fmt.Sprintf(
			"Applying fixture %s to %s (%d copies of %s into %s)",
			fixture.Preset,
			fixture.Target,
			fixture.Copies,
			fixture.Source,
			fixture.Name,
		)
	case scenario.FixturePresetBulkLogs:
		return fmt.Sprintf(
			"Applying fixture %s to %s (%d data streams, %d fields each)",
			fixture.Preset,
			fixture.Target,
			fixture.DataStreams,
			fixture.FieldsPerStream,
		)
	case scenario.FixturePresetBulkDocuments:
		timeRange := "one-second timestamp spacing"
		if fixture.TimeRange != "" {
			timeRange = fixture.TimeRange + " time range"
		}

		return fmt.Sprintf(
			"Applying fixture %s to %s (%d documents, %d extra fields, %d-byte messages, %s)",
			fixture.Preset,
			fixture.Target,
			fixture.Documents,
			fixture.FieldsPerDocument,
			fixture.MessageBytes,
			timeRange,
		)
	default:
		return fmt.Sprintf("Applying fixture %s to %s", fixture.Preset, fixture.Target)
	}
}

// applyFault turns one declared degradation into the matching Toxiproxy toxic.
func applyFault(ctx context.Context, proxyName string, fault scenario.Fault) error {
	switch fault.Type {
	case scenario.LatencyFaultType:
		return toxiproxy.ApplyLatency(
			ctx,
			proxyName,
			fault.ID,
			fault.LatencyMS,
			fault.JitterMS,
		)
	case scenario.BandwidthFaultType:
		return toxiproxy.ApplyBandwidth(ctx, proxyName, fault.ID, fault.RateKBps)
	default:
		return fmt.Errorf("fault type %q is not supported", fault.Type)
	}
}

// faultDescription states a fault in the units the reader cares about, since the
// two types cost in different ways: one per request, the other per byte.
func faultDescription(fault scenario.Fault) string {
	if fault.Type == scenario.BandwidthFaultType {
		return fmt.Sprintf("%d KB/s", fault.RateKBps)
	}

	return fmt.Sprintf("%dms latency, %dms jitter", fault.LatencyMS, fault.JitterMS)
}

// applyFaults activates the declared degradations after the healthy topology is ready.
func applyFaults(
	ctx context.Context,
	faults []scenario.Fault,
	proxyNamesByTarget map[string][]string,
	reportProgress func(string),
) error {
	for _, fault := range faults {
		proxyNames := proxyNamesByTarget[fault.Target]
		if len(proxyNames) == 0 {
			return fmt.Errorf("apply fault %s: target %q has no proxy", fault.ID, fault.Target)
		}

		selectedProxyNames := proxyNames
		if fault.AliasPosition > 0 {
			aliasIndex := fault.AliasPosition - 1
			if aliasIndex >= len(proxyNames) {
				return fmt.Errorf(
					"apply fault %s: alias position %d does not exist on target %q",
					fault.ID,
					fault.AliasPosition,
					fault.Target,
				)
			}

			selectedProxyNames = proxyNames[aliasIndex : aliasIndex+1]
		}

		reportProgress(
			fmt.Sprintf(
				"Applying fault %s (%s) to %s across %d of %d proxies",
				fault.ID,
				faultDescription(fault),
				fault.Target,
				len(selectedProxyNames),
				len(proxyNames),
			),
		)

		for _, proxyName := range selectedProxyNames {
			faultError := applyFault(ctx, proxyName, fault)
			if faultError != nil {
				return fmt.Errorf(
					"apply fault %s to proxy %s: %w",
					fault.ID,
					proxyName,
					faultError,
				)
			}
		}
	}

	return nil
}
