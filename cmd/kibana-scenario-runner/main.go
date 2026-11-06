package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/runner"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
	standardscenarios "github.com/bartoval/elasticsearch-degradation-lab/scenarios"
)

const usage = `Usage:
  kibana-scenario-runner validate <scenario-name|scenario.yml>
  kibana-scenario-runner play <scenario-name|scenario.yml> [--kibana-dir <path>]
  kibana-scenario-runner stop [scenario-name|scenario.yml]
  kibana-scenario-runner up <scenario-name|scenario.yml> [--kibana-dir <path>]
  kibana-scenario-runner --help`

const (
	exitSuccess          = 0
	exitFailure          = 1
	exitInvalidArguments = 2
)

const kibanaDirectoryOption = "--kibana-dir"

// Command-line arguments:
//
//	os.Args[0]: executable name
//	os.Args[1]: command, such as "validate" or "up"
//	os.Args[2]: built-in scenario name or scenario file path
//
// run receives os.Args[1:], so the command becomes args[0] and the scenario
// reference becomes args[1].
func main() {
	os.Exit(run(os.Args[1:]))
}

// run validates the command-line shape before dispatching the requested operation.
func run(args []string) int {
	if len(args) == 0 {
		return reportInvalidArguments(errors.New("missing command"))
	}

	command := args[0]

	switch command {
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, usage)

		return exitSuccess
	case "validate":
		if len(args) < 2 {
			return reportInvalidArguments(fmt.Errorf("missing scenario name or path for %s", command))
		}

		if len(args) > 2 {
			return reportInvalidArguments(fmt.Errorf("unexpected arguments for %s", command))
		}

		return runValidateCommand(args[1])
	case "up", "play":
		if len(args) < 2 {
			return reportInvalidArguments(fmt.Errorf("missing scenario name or path for %s", command))
		}

		kibanaDirectory, optionsError := parseStartOptions(command, args[2:])
		if optionsError != nil {
			return reportInvalidArguments(optionsError)
		}

		if command == "play" {
			return runPlayCommand(args[1], kibanaDirectory)
		}

		return runUpCommand(args[1], kibanaDirectory)
	case "stop":
		if len(args) > 2 {
			return reportInvalidArguments(errors.New("unexpected arguments for stop"))
		}

		if len(args) == 2 {
			return runStopCommand(args[1])
		}

		return runStopCommand("")
	default:
		return reportInvalidArguments(fmt.Errorf("unknown command %q", command))
	}
}

// reportInvalidArguments states the command-line problem next to the usage that
// would have avoided it.
func reportInvalidArguments(argumentError error) int {
	fmt.Fprintln(os.Stderr, argumentError)
	fmt.Fprintln(os.Stderr, usage)

	return exitInvalidArguments
}

// parseStartOptions reads the optional Kibana checkout without adding a CLI dependency.
func parseStartOptions(command string, arguments []string) (string, error) {
	if len(arguments) == 0 {
		return "", nil
	}

	if arguments[0] != kibanaDirectoryOption {
		return "", fmt.Errorf("unknown option %q for %s", arguments[0], command)
	}

	if len(arguments) == 1 {
		return "", fmt.Errorf("missing path for %s", kibanaDirectoryOption)
	}

	if len(arguments) > 2 {
		return "", fmt.Errorf("unexpected arguments for %s", command)
	}

	return arguments[1], nil
}

// runValidateCommand reports configuration problems as CLI output and an exit code.
func runValidateCommand(scenarioReference string) int {
	loadedScenario, loadError := loadScenario(scenarioReference)
	if loadError != nil {
		fmt.Fprintf(os.Stderr, "scenario validation failed: %v\n", loadError)

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Scenario %q is valid.\n", loadedScenario.ID)

	return exitSuccess
}

// runUpCommand resolves local paths before starting the scenario infrastructure.
func runUpCommand(scenarioReference string, configuredKibanaDirectory string) int {
	options, optionsError := resolveStartOptions(scenarioReference, configuredKibanaDirectory)
	if optionsError != nil {
		fmt.Fprintln(os.Stderr, optionsError)

		return exitFailure
	}

	// Convert Ctrl+C and SIGTERM into cancellation for startup and the active wait.
	// The environment owns child shutdown separately so it can preserve stop order.
	signalContext, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()
	runContext, cancelRun := context.WithCancel(signalContext)
	defer cancelRun()

	logPath := filepath.Dir(backgroundLogPath(options))
	if os.Getenv(backgroundEnvironment) == "1" {
		logPath = backgroundLogPath(options)
	}

	state := scenarioState{
		ScenarioID: options.Scenario.ID,
		LogPath:    logPath,
	}
	control, readyFile, controlError := startCommandControl(scenarioControlDirectory(), state, cancelRun)
	if controlError != nil {
		fmt.Fprintln(os.Stderr, controlError)

		return exitFailure
	}

	if readyFile != nil {
		defer readyFile.Close()
	}

	environment, startError := runner.Start(runContext, options)
	if startError != nil {
		completionError := startError
		if startError == runContext.Err() {
			completionError = nil
		}

		if closeError := control.close(completionError); closeError != nil {
			return reportStartupFailure(runContext, errors.Join(startError, closeError))
		}

		return reportStartupFailure(runContext, startError)
	}

	announceReady(options.Scenario.Name)
	readyError := notifyBackgroundReady(readyFile)
	if readyError != nil {
		cancelRun()
	}

	waitError := environment.Wait(runContext)
	if readyError != nil {
		waitError = errors.Join(readyError, waitError)
	}

	// Restore default signal handling, so a second Ctrl+C still works if the
	// teardown that follows turns out to be slow.
	stopSignals()

	fmt.Fprintln(os.Stdout, "\n[progress] Stopping scenario infrastructure")
	stopError := environment.Stop()
	stopError = errors.Join(stopError, control.close(errors.Join(waitError, stopError)))

	exitCode := exitSuccess
	if waitError != nil {
		exitCode = reportUnexpectedExit(waitError)
	}
	if stopError != nil {
		fmt.Fprintf(os.Stderr, "scenario cleanup failed: %v\n", stopError)
		exitCode = exitFailure
	}

	return exitCode
}

// loadScenario treats existing files and explicit paths as external scenarios;
// otherwise it resolves the argument from the catalog embedded in the binary.
func loadScenario(reference string) (scenario.Scenario, error) {
	if strings.ContainsAny(reference, `/\\`) {
		return scenario.Load(reference)
	}

	_, statError := os.Stat(reference)
	if statError == nil || !errors.Is(statError, os.ErrNotExist) {
		return scenario.Load(reference)
	}

	filename, contents, catalogError := standardscenarios.Read(reference)
	if catalogError != nil {
		return scenario.Scenario{}, catalogError
	}

	return scenario.LoadBytes(filename, contents)
}

// announceReady tells the operator the environment is theirs to drive.
func announceReady(scenarioName string) {
	fmt.Fprintf(os.Stdout, "\nScenario %q infrastructure is ready.\n", scenarioName)
	fmt.Fprintln(os.Stdout, "Elasticsearch and fixtures are ready, and the scenario is active.")
	fmt.Fprintln(os.Stdout, "\nStart Kibana separately using your normal development command.")
	fmt.Fprintln(os.Stdout, "\nRun kibana-scenario-runner stop to stop the environment.")
}

// reportStartupFailure treats user cancellation consistently throughout scenario preparation.
func reportStartupFailure(ctx context.Context, startupError error) int {
	if ctx.Err() != nil && startupError == ctx.Err() {
		fmt.Fprintln(os.Stdout, "Scenario startup cancelled.")

		return exitSuccess
	}

	fmt.Fprintln(os.Stderr, startupError)

	return exitFailure
}

// reportUnexpectedExit stops a session that can no longer represent the declared
// scenario, naming the log that explains why.
func reportUnexpectedExit(waitError error) int {
	fmt.Fprintf(os.Stderr, "\n%v\n", waitError)

	var exitError *runner.UnexpectedExitError
	if errors.As(waitError, &exitError) {
		fmt.Fprintf(os.Stderr, "Full log: %s\n", exitError.LogPath)
	}

	return exitFailure
}

func resolveStartOptions(scenarioReference string, configuredKibanaDirectory string) (runner.Options, error) {
	loadedScenario, loadError := loadScenario(scenarioReference)
	if loadError != nil {
		return runner.Options{}, fmt.Errorf("scenario validation failed: %w", loadError)
	}

	projectDirectory, workingDirectoryError := os.Getwd()
	if workingDirectoryError != nil {
		return runner.Options{}, fmt.Errorf("read project directory: %w", workingDirectoryError)
	}

	kibanaDirectory := configuredKibanaDirectory
	if kibanaDirectory == "" {
		kibanaDirectory = filepath.Join(projectDirectory, "..", "kibana")
	}

	absoluteKibanaDirectory, pathError := filepath.Abs(kibanaDirectory)
	if pathError != nil {
		return runner.Options{}, fmt.Errorf("resolve Kibana directory: %w", pathError)
	}

	return runner.Options{
		Scenario:         loadedScenario,
		KibanaDirectory:  absoluteKibanaDirectory,
		RuntimeDirectory: filepath.Join(projectDirectory, ".runtime"),
		ReportProgress:   reportProgress,
	}, nil
}

func reportProgress(message string) {
	fmt.Fprintf(os.Stdout, "[progress] %s\n", message)
}
