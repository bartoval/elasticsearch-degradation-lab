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
	"os"
	"path/filepath"
	"sync"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/elasticsearch"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/toxiproxy"
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
	toxiproxyServer  *toxiproxy.Server
	toxiproxyLog     string
	coordinatorNode  *elasticsearch.Node
	remoteNode       *elasticsearch.Node
	sessionDirectory string

	buildCancel  context.CancelCauseFunc
	failureDone  chan struct{}
	failureOnce  sync.Once
	failureError error
	stopping     chan struct{}
	stopOnce     sync.Once
	stopError    error
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
		toxiproxyServer:  toxiproxyServer,
		toxiproxyLog:     toxiproxyLog,
		sessionDirectory: sessionDirectory,
		failureDone:      make(chan struct{}),
		stopping:         make(chan struct{}),
	}
	buildContext, cancelBuild := context.WithCancelCause(ctx)
	environment.buildCancel = cancelBuild
	environment.monitorProcess(
		"Toxiproxy",
		toxiproxyLog,
		toxiproxyServer.Done(),
		toxiproxyServer.Wait,
	)

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
		remoteNode, remoteError := startElasticsearch(
			context.Background(),
			options.KibanaDirectory,
			sessionDirectory,
			"remote",
			nodeOptions(options.Scenario, remote.ID, remoteHTTPPort, remoteTransportPort),
		)
		if remoteError != nil {
			return remoteError
		}

		environment.remoteNode = remoteNode
		environment.monitorProcess(
			"remote Elasticsearch",
			remoteNode.LogPath(),
			remoteNode.Done(),
			remoteNode.Wait,
		)
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
	environment.coordinatorNode = coordinatorNode
	environment.monitorProcess(
		"coordinator Elasticsearch",
		coordinatorNode.LogPath(),
		coordinatorNode.Done(),
		coordinatorNode.Wait,
	)
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

// Wait holds the environment open until the caller cancels or a process dies. A
// death is reported rather than ignored, because an environment missing one of its
// processes can no longer represent the scenario it claims to.
func (environment *Environment) Wait(ctx context.Context) error {
	select {
	case <-environment.failureDone:
		return environment.failureError
	default:
	}

	select {
	case <-ctx.Done():
		select {
		case <-environment.failureDone:
			return environment.failureError
		default:
			return nil
		}
	case <-environment.failureDone:
		return environment.failureError
	}
}

// monitorProcess turns the first child death into one cached environment failure.
// It starts as soon as each child is launched, so fixture creation and all other
// build steps are supervised as well as the final interactive wait.
func (environment *Environment) monitorProcess(
	component string,
	logPath string,
	done <-chan struct{},
	wait func() error,
) {
	go func() {
		select {
		case <-environment.stopping:
			return
		case <-done:
		}

		select {
		case <-environment.stopping:
			return
		default:
		}

		exitError := &UnexpectedExitError{
			Component: component,
			LogPath:   logPath,
			Err:       wait(),
		}
		environment.failureOnce.Do(func() {
			environment.failureError = exitError
			close(environment.failureDone)
			environment.buildCancel(exitError)
		})
	}()
}

// Stop tears the environment down. Elasticsearch goes first and Toxiproxy last, so
// that no cluster is left addressing a proxy that has already gone. Every failure is
// reported rather than the first one stopping the rest.
func (environment *Environment) Stop() error {
	environment.stopOnce.Do(func() {
		close(environment.stopping)
		environment.buildCancel(context.Canceled)

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

		if len(stopErrors) == 0 {
			if cleanupError := cleanupSessionElasticsearch(environment.sessionDirectory); cleanupError != nil {
				stopErrors = append(stopErrors, cleanupError)
			}
		}

		environment.stopError = errors.Join(stopErrors...)
	})

	return environment.stopError
}

// cleanupSessionElasticsearch removes only the large per-run installation, data,
// and internal logs. Operator-facing logs live beside it and remain available.
func cleanupSessionElasticsearch(sessionDirectory string) error {
	elasticsearchDirectory := filepath.Join(sessionDirectory, "elasticsearch")
	if removeError := os.RemoveAll(elasticsearchDirectory); removeError != nil {
		return fmt.Errorf(
			"remove Elasticsearch runtime %q: %w",
			elasticsearchDirectory,
			removeError,
		)
	}

	return nil
}
