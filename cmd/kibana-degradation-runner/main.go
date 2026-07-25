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
	coordinatorHTTPPort      = 9200
	coordinatorTransportPort = 9300
	remoteHTTPPort           = 9201
	remoteTransportPort      = 9301
	remoteProxyPort          = 9401
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
		if runContext.Err() != nil {
			fmt.Fprintln(os.Stdout, "Scenario startup cancelled.")

			return exitSuccess
		}

		fmt.Fprintf(os.Stderr, "prepare Toxiproxy: %v\n", binaryError)

		return exitFailure
	}

	reportProgress("Starting Toxiproxy")

	toxiproxyLog := filepath.Join(sessionDirectory, "logs", "toxiproxy.log")
	toxiproxyServer, toxiproxyError := toxiproxy.Start(
		runContext,
		binaryPath,
		toxiproxyLog,
	)
	if toxiproxyError != nil {
		fmt.Fprintf(os.Stderr, "start Toxiproxy: %v\n", toxiproxyError)

		return exitFailure
	}
	defer toxiproxyServer.Stop()

	if readinessError := toxiproxyServer.WaitUntilReady(runContext); readinessError != nil {
		fmt.Fprintf(os.Stderr, "start Toxiproxy: %v\nFull log: %s\n", readinessError, toxiproxyLog)

		return exitFailure
	}

	reportProgress("Toxiproxy is ready at " + toxiproxy.APIURL)

	remote := loadedScenario.Topology.Remotes[0]
	reportProgress(
		fmt.Sprintf(
			"Creating transport proxy on port %d for remote %s",
			remoteProxyPort,
			remote.ID,
		),
	)

	proxyError := toxiproxy.CreateProxy(
		runContext,
		remote.ID,
		remoteProxyPort,
		remoteTransportPort,
	)
	if proxyError != nil {
		fmt.Fprintf(os.Stderr, "start scenario: %v\n", proxyError)

		return exitFailure
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
		fmt.Fprintf(os.Stderr, "start scenario: %v\n", remoteError)

		return exitFailure
	}
	defer remoteNode.Stop()

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
		fmt.Fprintf(os.Stderr, "start scenario: %v\n", coordinatorError)

		return exitFailure
	}
	defer coordinatorNode.Stop()

	reportProgress(
		fmt.Sprintf(
			"Configuring CCS alias %s through port %d",
			remote.Alias,
			remoteProxyPort,
		),
	)

	configurationError := coordinatorNode.ConfigureRemote(
		runContext,
		remote.Alias,
		remoteProxyPort,
		remote.SkipUnavailable,
	)
	if configurationError != nil {
		fmt.Fprintf(os.Stderr, "configure remote cluster: %v\n", configurationError)

		return exitFailure
	}

	connectionError := coordinatorNode.WaitForRemote(
		runContext,
		remote.Alias,
		reportProgress,
	)
	if connectionError != nil {
		fmt.Fprintf(os.Stderr, "connect remote cluster: %v\n", connectionError)

		return exitFailure
	}

	fixtureError := applyFixtures(
		runContext,
		loadedScenario.Fixtures,
		coordinatorNode,
		remoteNode,
		reportProgress,
	)
	if fixtureError != nil {
		fmt.Fprintf(os.Stderr, "%v\n", fixtureError)

		return exitFailure
	}

	faultError := applyFaults(runContext, loadedScenario.Faults, reportProgress)
	if faultError != nil {
		fmt.Fprintf(os.Stderr, "%v\n", faultError)

		return exitFailure
	}

	fmt.Fprintf(
		os.Stdout,
		"\nScenario %q infrastructure is ready.\n",
		loadedScenario.Name,
	)
	fmt.Fprintln(
		os.Stdout,
		"CCS is connected, fixtures are ready, and network faults are active.",
	)

	fmt.Fprintln(os.Stdout, "\nStart Kibana separately using your normal development command.")
	fmt.Fprintln(os.Stdout, "\nPress Ctrl+C here to stop the environment.")

	<-runContext.Done()
	stopSignals()

	fmt.Fprintln(os.Stdout, "\n[progress] Stopping scenario infrastructure")

	return exitSuccess
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
		node.Stop()

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

		reportProgress(
			fmt.Sprintf(
				"Applying fixture %s to %s",
				fixture.Preset,
				fixture.Target,
			),
		)

		var fixtureError error

		switch fixture.Preset {
		case scenario.FixturePresetMinimalLogs:
			fixtureError = targetNode.ApplyMinimalLogs(ctx)
		case scenario.FixturePresetMinimalLookup:
			fixtureError = targetNode.ApplyMinimalLookup(ctx)
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

// applyFaults activates the latency conditions after the healthy topology is ready.
func applyFaults(
	ctx context.Context,
	faults []scenario.Fault,
	reportProgress func(string),
) error {
	for _, fault := range faults {
		reportProgress(
			fmt.Sprintf(
				"Applying %dms latency fault %s to %s",
				fault.LatencyMS,
				fault.ID,
				fault.Target,
			),
		)

		faultError := toxiproxy.ApplyLatency(
			ctx,
			fault.Target,
			fault.ID,
			fault.LatencyMS,
			fault.JitterMS,
		)
		if faultError != nil {
			return fmt.Errorf("apply fault %s: %w", fault.ID, faultError)
		}
	}

	return nil
}
