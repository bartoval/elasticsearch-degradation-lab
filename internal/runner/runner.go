// Package runner brings a scenario up and holds it there.
//
// It owns the order in which the pieces start, the ports they occupy, and the
// decision that a session which has lost one of its processes is over. What it
// deliberately does not own is how any of that is presented: progress arrives
// through a callback and failures arrive as errors, so the same environment can be
// driven by a terminal today without that choice being baked in.
package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/elasticsearch"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
)

const (
	// coordinatorProxyPort is where Kibana connects, because it is Elasticsearch's
	// usual port and Kibana needs no configuration to find it. The coordinator
	// itself listens elsewhere, so every request Kibana makes crosses a proxy and
	// none of them can quietly bypass it.
	coordinatorProxyPort = 9200

	coordinatorHTTPPort      = 9210
	coordinatorTransportPort = 9300
	remoteHTTPPort           = 9201
	remoteTransportPort      = 9301

	// remoteProxyBasePort opens the range reserved for per-alias proxies: alias
	// number N listens on remoteProxyBasePort+N-1. The end of that range is what
	// scenario.MaxRemoteAliases bounds aliasCount to.
	remoteProxyBasePort = 9401
)

// Options describes the environment to build.
type Options struct {
	Scenario         scenario.Scenario
	KibanaDirectory  string
	RuntimeDirectory string
	// ReportProgress receives one line per step, so that a caller can show the long
	// waits rather than leaving them silent.
	ReportProgress func(string)
}

func (options Options) withDefaults() Options {
	if options.ReportProgress == nil {
		options.ReportProgress = func(string) {}
	}

	return options
}

// Start builds the environment and returns once it is ready to be used. On failure
// it takes back whatever it had already started, so the caller is never handed a
// half-built environment to clean up.
func Start(ctx context.Context, options Options) (*Environment, error) {
	options = options.withDefaults()

	if validationError := options.Scenario.Validate(); validationError != nil {
		return nil, fmt.Errorf("validate scenario: %w", validationError)
	}

	sessionDirectory := filepath.Join(options.RuntimeDirectory, options.Scenario.ID)
	toxiproxyLog := filepath.Join(sessionDirectory, "logs", "toxiproxy.log")

	toxiproxyServer, toxiproxyError := startToxiproxy(
		ctx,
		options.RuntimeDirectory,
		toxiproxyLog,
		options.ReportProgress,
	)
	if toxiproxyError != nil {
		if ctx.Err() != nil && errors.Is(toxiproxyError, ctx.Err()) {
			return nil, ctx.Err()
		}

		return nil, toxiproxyError
	}

	environment := &Environment{
		sessionDirectory: sessionDirectory,
		failureDone:      make(chan struct{}),
		stopping:         make(chan struct{}),
	}
	buildContext, cancelBuild := context.WithCancelCause(ctx)
	environment.buildCancel = cancelBuild
	environment.registerComponent(managedComponent{
		name:    "Toxiproxy",
		logPath: toxiproxyLog,
		done:    toxiproxyServer.Done(),
		wait:    toxiproxyServer.Wait,
		stop:    toxiproxyServer.Stop,
	})

	buildError := environment.build(buildContext, options, sessionDirectory)
	if buildError == nil {
		select {
		case <-environment.failureDone:
			buildError = environment.failureError
		default:
		}
	}
	if buildError != nil {
		select {
		case <-environment.failureDone:
			buildError = environment.failureError
		default:
			if ctx.Err() != nil {
				buildError = ctx.Err()
			}
		}

		// Undo the part that did start, since a caller holding an error has no
		// handle to stop it with.
		if stopError := environment.Stop(); stopError != nil {
			return nil, errors.Join(buildError, fmt.Errorf("cleanup failed: %w", stopError))
		}

		return nil, buildError
	}

	return environment, nil
}

// build performs the steps after Toxiproxy is up, so that Start can undo them all
// through one path when any of them fails.
func (environment *Environment) build(
	ctx context.Context,
	options Options,
	sessionDirectory string,
) error {
	hasRemote := len(options.Scenario.Topology.Remotes) == 1
	var remote scenario.Remote
	var remoteNode *elasticsearch.Node
	var aliasBindings []elasticsearch.RemoteAliasBinding

	if hasRemote {
		remote = options.Scenario.Topology.Remotes[0]
		aliasBindings = remoteAliasBindings(remote)
	}

	if proxyError := createProxies(ctx, aliasBindings, options.ReportProgress); proxyError != nil {
		return proxyError
	}

	if hasRemote {
		options.ReportProgress(
			fmt.Sprintf(
				"Starting remote Elasticsearch (HTTP %d, transport %d)",
				remoteHTTPPort,
				remoteTransportPort,
			),
		)
		var remoteError error
		remoteNode, remoteError = startElasticsearch(
			context.Background(),
			options.KibanaDirectory,
			sessionDirectory,
			"remote",
			nodeOptions(options.Scenario, remote.ID, remoteHTTPPort, remoteTransportPort),
		)
		if remoteError != nil {
			return remoteError
		}

		environment.registerComponent(managedComponent{
			name:    "remote Elasticsearch",
			logPath: remoteNode.LogPath(),
			done:    remoteNode.Done(),
			wait:    remoteNode.Wait,
			stop:    remoteNode.Stop,
		})
		if readinessError := waitForElasticsearch(
			ctx,
			remoteNode,
			"remote",
			options.ReportProgress,
		); readinessError != nil {
			return readinessError
		}
	}

	options.ReportProgress(
		fmt.Sprintf(
			"Starting %s Elasticsearch (HTTP %d, transport %d)",
			scenario.CoordinatorTarget,
			coordinatorHTTPPort,
			coordinatorTransportPort,
		),
	)
	coordinatorNode, coordinatorError := startElasticsearch(
		context.Background(),
		options.KibanaDirectory,
		sessionDirectory,
		scenario.CoordinatorTarget,
		nodeOptions(
			options.Scenario,
			scenario.CoordinatorTarget,
			coordinatorHTTPPort,
			coordinatorTransportPort,
		),
	)
	if coordinatorError != nil {
		return coordinatorError
	}
	environment.registerComponent(managedComponent{
		name:    "coordinator Elasticsearch",
		logPath: coordinatorNode.LogPath(),
		done:    coordinatorNode.Done(),
		wait:    coordinatorNode.Wait,
		stop:    coordinatorNode.Stop,
	})
	if readinessError := waitForElasticsearch(
		ctx,
		coordinatorNode,
		scenario.CoordinatorTarget,
		options.ReportProgress,
	); readinessError != nil {
		return readinessError
	}

	if hasRemote {
		connectionError := connectRemoteAliases(
			ctx,
			coordinatorNode,
			aliasBindings,
			options.ReportProgress,
		)
		if connectionError != nil {
			return connectionError
		}
	}

	fixtureError := applyFixtures(
		ctx,
		options.Scenario.Fixtures,
		coordinatorNode,
		remoteNode,
		options.ReportProgress,
	)
	if fixtureError != nil {
		return fixtureError
	}

	// Faults come last, so that everything before them ran against a healthy
	// topology and a failure there cannot be blamed on the degradation.
	proxyNamesByTarget := map[string][]string{
		scenario.CoordinatorTarget: {scenario.CoordinatorTarget},
	}

	if hasRemote {
		proxyNamesByTarget[remote.ID] = bindingAliases(aliasBindings)
	}

	return applyFaults(ctx, options.Scenario.Faults, proxyNamesByTarget, options.ReportProgress)
}

// nodeOptions gives both clusters the same scenario tuning, since a limit that only
// one of them honoured would make a measurement depend on which node answered.
func nodeOptions(
	scenarioConfig scenario.Scenario,
	id string,
	httpPort int,
	transportPort int,
) elasticsearch.Options {
	return elasticsearch.Options{
		ID:            id,
		HTTPPort:      httpPort,
		TransportPort: transportPort,
		Heap:          scenarioConfig.Elasticsearch.Heap,
		ExtraSettings: scenarioConfig.Elasticsearch.Settings,
	}
}
