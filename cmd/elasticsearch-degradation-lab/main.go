package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/runner"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
)

const usage = `Usage:
  elasticsearch-degradation-lab validate <scenario.yml>
  elasticsearch-degradation-lab up <scenario.yml> [--kibana-dir <path>]
  elasticsearch-degradation-lab --help`

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
		return reportInvalidArguments(errors.New("missing command"))
	}

	command := args[0]

	switch command {
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, usage)

		return exitSuccess
	case "validate":
		if len(args) < 2 {
			return reportInvalidArguments(fmt.Errorf("missing scenario path for %s", command))
		}

		if len(args) > 2 {
			return reportInvalidArguments(fmt.Errorf("unexpected arguments for %s", command))
		}

		return runValidateCommand(args[1])
	case "up":
		if len(args) < 2 {
			return reportInvalidArguments(fmt.Errorf("missing scenario path for %s", command))
		}

		kibanaDirectory, optionsError := parseUpOptions(args[2:])
		if optionsError != nil {
			return reportInvalidArguments(optionsError)
		}

		return runUpCommand(args[1], kibanaDirectory)
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
		return "", errors.New("unexpected arguments for up")
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

	// Convert Ctrl+C and SIGTERM into shared cancellation so downloads, readiness
	// checks, and every child process can stop together.
	runContext, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	environment, startError := runner.Start(runContext, runner.Options{
		Scenario:         loadedScenario,
		KibanaDirectory:  kibanaDirectory,
		RuntimeDirectory: filepath.Join(projectDirectory, ".runtime"),
		ReportProgress: func(message string) {
			fmt.Fprintf(os.Stdout, "[progress] %s\n", message)
		},
	})
	if startError != nil {
		return reportStartupFailure(runContext, startError)
	}
	defer reportStopFailure(environment.Stop)

	announceReady(loadedScenario.Name)

	waitError := environment.Wait(runContext)

	// Restore default signal handling, so a second Ctrl+C still works if the
	// teardown that follows turns out to be slow.
	stopSignals()

	if waitError != nil {
		return reportUnexpectedExit(waitError)
	}

	fmt.Fprintln(os.Stdout, "\n[progress] Stopping scenario infrastructure")

	return exitSuccess
}

// announceReady tells the operator the environment is theirs to drive.
func announceReady(scenarioName string) {
	fmt.Fprintf(os.Stdout, "\nScenario %q infrastructure is ready.\n", scenarioName)
	fmt.Fprintln(os.Stdout, "Elasticsearch and fixtures are ready, and the scenario is active.")
	fmt.Fprintln(os.Stdout, "\nStart Kibana separately using your normal development command.")
	fmt.Fprintln(os.Stdout, "\nPress Ctrl+C here to stop the environment.")
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

// reportStopFailure keeps cleanup problems visible without hiding the original result.
func reportStopFailure(stop func() error) {
	if stopError := stop(); stopError != nil {
		fmt.Fprintf(os.Stderr, "%v\n", stopError)
	}
}
