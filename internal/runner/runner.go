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

	"kibana-degradation-runner/internal/elasticsearch"
	"kibana-degradation-runner/internal/scenario"
	"kibana-degradation-runner/internal/toxiproxy"
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

// Environment is a running scenario: every process started, connected and degraded
// as the scenario asked.
type Environment struct {
	toxiproxyServer *toxiproxy.Server
	toxiproxyLog    string
	coordinatorNode *elasticsearch.Node
	remoteNode      *elasticsearch.Node
}

// UnexpectedExitError reports a process that ended while the scenario was still
// meant to be running. It is a distinct type because it means something different
// from a startup failure: the environment was correct and then stopped being so.
type UnexpectedExitError struct {
	Component string
	LogPath   string
	Err       error
}

func (exitError *UnexpectedExitError) Error() string {
	if exitError.Err == nil {
		return fmt.Sprintf("%s exited while the scenario was active", exitError.Component)
	}

	return fmt.Sprintf(
		"%s exited while the scenario was active: %v",
		exitError.Component,
		exitError.Err,
	)
}

func (exitError *UnexpectedExitError) Unwrap() error {
	return exitError.Err
}

// Start builds the environment and returns once it is ready to be used. On failure
// it takes back whatever it had already started, so the caller is never handed a
// half-built environment to clean up.
func Start(ctx context.Context, options Options) (*Environment, error) {
	sessionDirectory := filepath.Join(options.RuntimeDirectory, options.Scenario.ID)
	toxiproxyLog := filepath.Join(sessionDirectory, "logs", "toxiproxy.log")

	toxiproxyServer, toxiproxyError := startToxiproxy(
		ctx,
		options.RuntimeDirectory,
		toxiproxyLog,
		options.ReportProgress,
	)
	if toxiproxyError != nil {
		return nil, toxiproxyError
	}

	environment := &Environment{
		toxiproxyServer: toxiproxyServer,
		toxiproxyLog:    toxiproxyLog,
	}

	if buildError := environment.build(ctx, options, sessionDirectory); buildError != nil {
		// Undo the part that did start, since a caller holding an error has no
		// handle to stop it with.
		_ = environment.Stop()

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
	var aliasBindings []elasticsearch.RemoteAliasBinding

	if hasRemote {
		remote = options.Scenario.Topology.Remotes[0]
		aliasBindings = remoteAliasBindings(remote)
	}

	if proxyError := createProxies(ctx, aliasBindings, options.ReportProgress); proxyError != nil {
		return proxyError
	}

	if hasRemote {
		remoteNode, remoteError := startElasticsearch(
			ctx,
			options.KibanaDirectory,
			sessionDirectory,
			"remote",
			elasticsearch.Options{
				ID:            remote.ID,
				HTTPPort:      remoteHTTPPort,
				TransportPort: remoteTransportPort,
			},
			options.ReportProgress,
		)
		if remoteError != nil {
			return remoteError
		}

		environment.remoteNode = remoteNode
	}

	coordinatorNode, coordinatorError := startElasticsearch(
		ctx,
		options.KibanaDirectory,
		sessionDirectory,
		scenario.CoordinatorTarget,
		elasticsearch.Options{
			ID:            scenario.CoordinatorTarget,
			HTTPPort:      coordinatorHTTPPort,
			TransportPort: coordinatorTransportPort,
		},
		options.ReportProgress,
	)
	if coordinatorError != nil {
		return coordinatorError
	}
	environment.coordinatorNode = coordinatorNode

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
		environment.remoteNode,
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

// Wait holds the environment open until the caller cancels or a process dies. A
// death is reported rather than ignored, because an environment missing one of its
// processes can no longer represent the scenario it claims to.
func (environment *Environment) Wait(ctx context.Context) error {
	var remoteExited <-chan error
	if environment.remoteNode != nil {
		remoteExited = environment.remoteNode.Exited()
	}

	select {
	case <-ctx.Done():
		return nil
	case processError := <-environment.toxiproxyServer.Exited():
		return environment.unexpectedExit(ctx, "Toxiproxy", environment.toxiproxyLog, processError)
	case processError := <-remoteExited:
		return environment.unexpectedExit(
			ctx,
			"remote Elasticsearch",
			environment.remoteNode.LogPath(),
			processError,
		)
	case processError := <-environment.coordinatorNode.Exited():
		return environment.unexpectedExit(
			ctx,
			"coordinator Elasticsearch",
			environment.coordinatorNode.LogPath(),
			processError,
		)
	}
}

// unexpectedExit distinguishes a process that died on its own from one that exited
// because the caller had already asked the environment to stop.
func (environment *Environment) unexpectedExit(
	ctx context.Context,
	component string,
	logPath string,
	processError error,
) error {
	if ctx.Err() != nil {
		return nil
	}

	return &UnexpectedExitError{Component: component, LogPath: logPath, Err: processError}
}

// Stop tears the environment down. Elasticsearch goes first and Toxiproxy last, so
// that no cluster is left addressing a proxy that has already gone. Every failure is
// reported rather than the first one stopping the rest.
func (environment *Environment) Stop() error {
	var stopErrors []error

	if environment.coordinatorNode != nil {
		if stopError := environment.coordinatorNode.Stop(); stopError != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop coordinator Elasticsearch: %w", stopError))
		}
	}

	if environment.remoteNode != nil {
		if stopError := environment.remoteNode.Stop(); stopError != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop remote Elasticsearch: %w", stopError))
		}
	}

	if environment.toxiproxyServer != nil {
		if stopError := environment.toxiproxyServer.Stop(); stopError != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop Toxiproxy: %w", stopError))
		}
	}

	return errors.Join(stopErrors...)
}
