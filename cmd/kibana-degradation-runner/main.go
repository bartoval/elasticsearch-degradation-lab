package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"kibana-degradation-runner/internal/elasticsearch"
	"kibana-degradation-runner/internal/scenario"
	"kibana-degradation-runner/internal/toxiproxy"
)

const usage = `Usage:
  kibana-degradation-runner validate <scenario.yml>
  kibana-degradation-runner up <scenario.yml> [--kibana-dir <path>]
  kibana-degradation-runner --help`

const (
	exitSuccess          = 0
	exitFailure          = 1
	exitInvalidArguments = 2
)

const kibanaDirectoryOption = "--kibana-dir"

const (
	// coordinatorProxyPort is where Kibana connects, because it is Elasticsearch's
	// usual port and Kibana needs no configuration to find it. The coordinator
	// itself listens one range higher, so every request Kibana makes crosses a
	// proxy and none of them can quietly bypass it.
	coordinatorProxyPort = 9200

	coordinatorHTTPPort      = 9210
	coordinatorTransportPort = 9300
	remoteHTTPPort           = 9201
	remoteTransportPort      = 9301

	// remoteProxyBasePort opens the range reserved for per-alias proxies: alias
	// number N listens on remoteProxyBasePort+N-1. The range ends at 9800, which
	// is what scenario.MaxRemoteAliases bounds aliasCount to.
	remoteProxyBasePort = 9401
)

// Command-line arguments:
//
//	os.Args[0]: executable name
//	os.Args[1]: command, such as "validate" or "up"
//	os.Args[2]: scenario file path
//
// run receives os.Args[1:], so the command becomes args[0] and the scenario
// file path becomes args[1].
func main() {
	os.Exit(run(os.Args[1:]))
}

// run validates the command-line shape before dispatching the requested operation.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing command")
		fmt.Fprintln(os.Stderr, usage)

		return exitInvalidArguments
	}

	command := args[0]

	switch command {
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, usage)

		return exitSuccess
	case "validate", "up":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "missing scenario path for %s\n", command)
			fmt.Fprintln(os.Stderr, usage)

			return exitInvalidArguments
		}

		scenarioPath := args[1]
		options := args[2:]

		if command == "validate" && len(options) > 0 {
			fmt.Fprintf(os.Stderr, "unexpected arguments for %s\n", command)
			fmt.Fprintln(os.Stderr, usage)

			return exitInvalidArguments
		}

		if command == "validate" {
			return runValidateCommand(scenarioPath)
		}

		kibanaDirectory, optionsError := parseUpOptions(options)
		if optionsError != nil {
			fmt.Fprintln(os.Stderr, optionsError)
			fmt.Fprintln(os.Stderr, usage)

			return exitInvalidArguments
		}

		return runUpCommand(scenarioPath, kibanaDirectory)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		fmt.Fprintln(os.Stderr, usage)

		return exitInvalidArguments
	}
}

// parseUpOptions reads the optional Kibana checkout without adding a CLI dependency.
func parseUpOptions(arguments []string) (string, error) {
	if len(arguments) == 0 {
		return "", nil
	}

	if arguments[0] != kibanaDirectoryOption {
		return "", fmt.Errorf("unknown option %q for up", arguments[0])
	}

	if len(arguments) == 1 {
		return "", fmt.Errorf("missing path for %s", kibanaDirectoryOption)
	}

	if len(arguments) > 2 {
		return "", fmt.Errorf("unexpected arguments for up")
	}

	return arguments[1], nil
}

// runValidateCommand reports configuration problems as CLI output and an exit code.
func runValidateCommand(scenarioPath string) int {
	loadedScenario, loadError := scenario.Load(scenarioPath)
	if loadError != nil {
		fmt.Fprintf(os.Stderr, "scenario validation failed: %v\n", loadError)

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Scenario %q is valid.\n", loadedScenario.ID)

	return exitSuccess
}

// runUpCommand resolves local paths before starting the scenario infrastructure.
func runUpCommand(scenarioPath string, configuredKibanaDirectory string) int {
	loadedScenario, loadError := scenario.Load(scenarioPath)
	if loadError != nil {
		fmt.Fprintf(os.Stderr, "scenario validation failed: %v\n", loadError)

		return exitFailure
	}

	projectDirectory, workingDirectoryError := os.Getwd()
	if workingDirectoryError != nil {
		fmt.Fprintf(os.Stderr, "read project directory: %v\n", workingDirectoryError)

		return exitFailure
	}

	kibanaDirectory := configuredKibanaDirectory
	if kibanaDirectory == "" {
		kibanaDirectory = filepath.Join(projectDirectory, "..", "kibana")
	}

	absoluteKibanaDirectory, pathError := filepath.Abs(kibanaDirectory)
	if pathError != nil {
		fmt.Fprintf(os.Stderr, "resolve Kibana directory: %v\n", pathError)

		return exitFailure
	}

	kibanaDirectory = absoluteKibanaDirectory

	// Convert Ctrl+C and SIGTERM into shared cancellation so downloads,
	// readiness checks, and every child process can stop together.
	runContext, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	// This callback lets internal packages report progress without depending on CLI output.
	reportProgress := func(message string) {
		fmt.Fprintf(os.Stdout, "[progress] %s\n", message)
	}

	runtimeDirectory := filepath.Join(projectDirectory, ".runtime")
	sessionDirectory := filepath.Join(runtimeDirectory, loadedScenario.ID)

	binaryPath, binaryError := toxiproxy.EnsureBinary(
		runContext,
		runtimeDirectory,
		reportProgress,
	)
	if binaryError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("prepare Toxiproxy: %w", binaryError),
		)
	}

	reportProgress("Starting Toxiproxy")

	toxiproxyLog := filepath.Join(sessionDirectory, "logs", "toxiproxy.log")
	toxiproxyServer, toxiproxyError := toxiproxy.Start(
		runContext,
		binaryPath,
		toxiproxyLog,
	)
	if toxiproxyError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("start Toxiproxy: %w", toxiproxyError),
		)
	}
	defer toxiproxyServer.Stop()

	if readinessError := toxiproxyServer.WaitUntilReady(runContext); readinessError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("start Toxiproxy: %w\nFull log: %s", readinessError, toxiproxyLog),
		)
	}

	reportProgress("Toxiproxy is ready at " + toxiproxy.APIURL)

	remote := loadedScenario.Topology.Remotes[0]
	remoteAliases := remote.Aliases()
	lastProxyPort := remoteProxyBasePort + len(remoteAliases) - 1
	reportProgress(
		fmt.Sprintf(
			"Creating %d transport proxies on ports %d-%d for remote %s",
			len(remoteAliases),
			remoteProxyBasePort,
			lastProxyPort,
			remote.ID,
		),
	)

	reportProgress(
		fmt.Sprintf(
			"Creating coordinator proxy on port %d, which Kibana should use",
			coordinatorProxyPort,
		),
	)

	// Kibana reaches the coordinator through a proxy of its own, so that a fault
	// can degrade what Kibana asks Elasticsearch directly, saved objects included,
	// rather than only what crosses to the remotes.
	coordinatorProxyError := toxiproxy.CreateProxy(
		runContext,
		scenario.CoordinatorTarget,
		coordinatorProxyPort,
		coordinatorHTTPPort,
	)
	if coordinatorProxyError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("start scenario: coordinator proxy: %w", coordinatorProxyError),
		)
	}

	// Each alias gets its own proxy in front of the same physical transport port.
	// Cross-cluster fan-out runs in parallel, so a wide topology only hurts when
	// the aliases can be degraded independently and one of them lands in the tail.
	for aliasIndex, alias := range remoteAliases {
		proxyError := toxiproxy.CreateProxy(
			runContext,
			alias,
			remoteProxyBasePort+aliasIndex,
			remoteTransportPort,
		)
		if proxyError != nil {
			return reportStartupFailure(
				runContext,
				fmt.Errorf("start scenario: proxy for alias %s: %w", alias, proxyError),
			)
		}
	}

	remoteNode, remoteError := startElasticsearch(
		runContext,
		kibanaDirectory,
		sessionDirectory,
		"remote",
		elasticsearch.Options{
			ID:            remote.ID,
			HTTPPort:      remoteHTTPPort,
			TransportPort: remoteTransportPort,
		},
		reportProgress,
	)
	if remoteError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("start scenario: %w", remoteError),
		)
	}
	defer reportStopFailure("remote Elasticsearch", remoteNode.Stop)

	coordinatorNode, coordinatorError := startElasticsearch(
		runContext,
		kibanaDirectory,
		sessionDirectory,
		scenario.CoordinatorTarget,
		elasticsearch.Options{
			ID:            scenario.CoordinatorTarget,
			HTTPPort:      coordinatorHTTPPort,
			TransportPort: coordinatorTransportPort,
		},
		reportProgress,
	)
	if coordinatorError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("start scenario: %w", coordinatorError),
		)
	}
	defer reportStopFailure("coordinator Elasticsearch", coordinatorNode.Stop)

	reportProgress(
		fmt.Sprintf(
			"Configuring %d CCS aliases (%s .. %s) through ports %d-%d",
			len(remoteAliases),
			remoteAliases[0],
			remoteAliases[len(remoteAliases)-1],
			remoteProxyBasePort,
			lastProxyPort,
		),
	)

	aliasBindings := make([]elasticsearch.RemoteAliasBinding, len(remoteAliases))
	for aliasIndex, alias := range remoteAliases {
		aliasBindings[aliasIndex] = elasticsearch.RemoteAliasBinding{
			Alias:           alias,
			ProxyPort:       remoteProxyBasePort + aliasIndex,
			SkipUnavailable: remote.SkipUnavailable,
		}
	}

	configurationError := coordinatorNode.ConfigureRemoteAliases(
		runContext,
		aliasBindings,
	)
	if configurationError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("configure remote cluster: %w", configurationError),
		)
	}

	connectionError := coordinatorNode.WaitForRemoteAliases(
		runContext,
		remoteAliases,
		reportProgress,
	)
	if connectionError != nil {
		return reportStartupFailure(
			runContext,
			fmt.Errorf("connect remote cluster: %w", connectionError),
		)
	}

	fixtureError := applyFixtures(
		runContext,
		loadedScenario.Fixtures,
		coordinatorNode,
		remoteNode,
		reportProgress,
	)
	if fixtureError != nil {
		return reportStartupFailure(runContext, fixtureError)
	}

	proxyNamesByTarget := map[string][]string{
		remote.ID:                  remoteAliases,
		scenario.CoordinatorTarget: {scenario.CoordinatorTarget},
	}

	faultError := applyFaults(
		runContext,
		loadedScenario.Faults,
		proxyNamesByTarget,
		reportProgress,
	)
	if faultError != nil {
		return reportStartupFailure(runContext, faultError)
	}

	fmt.Fprintf(
		os.Stdout,
		"\nScenario %q infrastructure is ready.\n",
		loadedScenario.Name,
	)
	fmt.Fprintln(
		os.Stdout,
		"CCS is connected, fixtures are ready, and the scenario is active.",
	)

	fmt.Fprintln(os.Stdout, "\nStart Kibana separately using your normal development command.")
	fmt.Fprintln(os.Stdout, "\nPress Ctrl+C here to stop the environment.")

	select {
	case <-runContext.Done():
		stopSignals()
	case processError := <-toxiproxyServer.Exited():
		if runContext.Err() == nil {
			return reportUnexpectedExit("Toxiproxy", processError, toxiproxyLog)
		}
	case processError := <-remoteNode.Exited():
		if runContext.Err() == nil {
			return reportUnexpectedExit("remote Elasticsearch", processError, remoteNode.LogPath())
		}
	case processError := <-coordinatorNode.Exited():
		if runContext.Err() == nil {
			return reportUnexpectedExit(
				"coordinator Elasticsearch",
				processError,
				coordinatorNode.LogPath(),
			)
		}
	}

	fmt.Fprintln(os.Stdout, "\n[progress] Stopping scenario infrastructure")

	return exitSuccess
}

// reportStartupFailure treats user cancellation consistently throughout scenario preparation.
func reportStartupFailure(ctx context.Context, startupError error) int {
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stdout, "Scenario startup cancelled.")

		return exitSuccess
	}

	fmt.Fprintln(os.Stderr, startupError)

	return exitFailure
}

// reportUnexpectedExit stops a session that can no longer represent the declared scenario.
func reportUnexpectedExit(component string, processError error, logPath string) int {
	fmt.Fprintf(os.Stderr, "\n%s exited while the scenario was active", component)
	if processError != nil {
		fmt.Fprintf(os.Stderr, ": %v", processError)
	}
	fmt.Fprintf(os.Stderr, "\nFull log: %s\n", logPath)

	return exitFailure
}

// reportStopFailure keeps cleanup problems visible without hiding the original result.
func reportStopFailure(component string, stop func() error) {
	if stopError := stop(); stopError != nil {
		fmt.Fprintf(os.Stderr, "stop %s: %v\n", component, stopError)
	}
}

// startElasticsearch gives both local clusters identical progress and error handling.
func startElasticsearch(
	ctx context.Context,
	kibanaDirectory string,
	sessionDirectory string,
	role string,
	options elasticsearch.Options,
	reportProgress func(string),
) (*elasticsearch.Node, error) {
	reportProgress(
		fmt.Sprintf(
			"Starting %s Elasticsearch (HTTP %d, transport %d)",
			role,
			options.HTTPPort,
			options.TransportPort,
		),
	)

	node, startError := elasticsearch.Start(
		ctx,
		kibanaDirectory,
		sessionDirectory,
		options,
	)
	if startError != nil {
		return nil, fmt.Errorf("start %s Elasticsearch: %w", role, startError)
	}

	reportProgress(fmt.Sprintf("%s Elasticsearch log: %s", role, node.LogPath()))

	if readinessError := node.WaitUntilReady(ctx, reportProgress); readinessError != nil {
		stopError := node.Stop()
		if stopError != nil {
			return nil, fmt.Errorf(
				"wait for %s Elasticsearch: %w; cleanup failed: %v",
				role,
				readinessError,
				stopError,
			)
		}

		return nil, fmt.Errorf("wait for %s Elasticsearch: %w", role, readinessError)
	}

	return node, nil
}

// applyFixtures sends each data preset to the target selected by the scenario.
func applyFixtures(
	ctx context.Context,
	fixtures []scenario.Fixture,
	coordinatorNode *elasticsearch.Node,
	remoteNode *elasticsearch.Node,
	reportProgress func(string),
) error {
	for _, fixture := range fixtures {
		targetNode := remoteNode
		if fixture.Target == scenario.CoordinatorTarget {
			targetNode = coordinatorNode
		}

		reportProgress(fixtureDescription(fixture))

		var fixtureError error

		switch fixture.Preset {
		case scenario.FixturePresetMinimalLogs:
			fixtureError = targetNode.ApplyMinimalLogs(ctx, fixture.Name)
		case scenario.FixturePresetMinimalLookup:
			fixtureError = targetNode.ApplyMinimalLookup(ctx, fixture.Name)
		case scenario.FixturePresetBulkLogs:
			fixtureError = targetNode.ApplyBulkLogs(
				ctx,
				fixture.Name,
				fixture.DataStreams,
				fixture.FieldsPerStream,
				reportProgress,
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
	if fixture.Preset != scenario.FixturePresetBulkLogs {
		return fmt.Sprintf("Applying fixture %s to %s", fixture.Preset, fixture.Target)
	}

	return fmt.Sprintf(
		"Applying fixture %s to %s (%d indices, %d fields each)",
		fixture.Preset,
		fixture.Target,
		fixture.DataStreams,
		fixture.FieldsPerStream,
	)
}

// applyFault turns one declared degradation into the matching Toxiproxy toxic.
func applyFault(ctx context.Context, proxyName string, fault scenario.Fault) error {
	switch fault.Type {
	case scenario.BandwidthFaultType:
		return toxiproxy.ApplyBandwidth(ctx, proxyName, fault.ID, fault.RateKBps)
	default:
		return toxiproxy.ApplyLatency(
			ctx,
			proxyName,
			fault.ID,
			fault.LatencyMS,
			fault.JitterMS,
		)
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

		reportProgress(
			fmt.Sprintf(
				"Applying fault %s (%s) to %s across %d proxies",
				fault.ID,
				faultDescription(fault),
				fault.Target,
				len(proxyNames),
			),
		)

		for _, proxyName := range proxyNames {
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
