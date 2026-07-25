package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"kibana-degradation-runner/internal/scenario"
	"kibana-degradation-runner/internal/toxiproxy"
)

const usage = `Usage:
  kibana-degradation-runner validate <scenario.yml>
  kibana-degradation-runner up <scenario.yml>
  kibana-degradation-runner --help`

const (
	exitSuccess          = 0
	exitFailure          = 1
	exitInvalidArguments = 2
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

		if len(args) > 2 {
			fmt.Fprintf(os.Stderr, "unexpected arguments for %s\n", command)
			fmt.Fprintln(os.Stderr, usage)

			return exitInvalidArguments
		}

		scenarioPath := args[1]

		if command == "validate" {
			return runValidateCommand(scenarioPath)
		}

		return runUpCommand(scenarioPath)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		fmt.Fprintln(os.Stderr, usage)

		return exitInvalidArguments
	}
}

// runValidateCommand reports configuration problems as CLI output and an exit code.
func runValidateCommand(scenarioPath string) int {
	loadedScenario, err := scenario.Load(scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario validation failed: %v\n", err)

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Scenario %q is valid.\n", loadedScenario.ID)

	return exitSuccess
}

// runUpCommand keeps Toxiproxy alive until the developer stops the environment with Ctrl+C.
func runUpCommand(scenarioPath string) int {
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

	// Convert Ctrl+C and SIGTERM into shared cancellation so downloads,
	// readiness checks, and the Toxiproxy process can stop together.
	runContext, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	runtimeDirectory := filepath.Join(projectDirectory, ".runtime")

	// This callback lets the Toxiproxy package report progress without depending on CLI output.
	reportProgress := func(message string) {
		fmt.Fprintf(os.Stdout, "[progress] %s\n", message)
	}

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

	logPath := filepath.Join(
		runtimeDirectory,
		loadedScenario.ID,
		"logs",
		"toxiproxy.log",
	)

	fmt.Fprintln(os.Stdout, "[progress] Starting Toxiproxy")

	toxiproxyServer, startError := toxiproxy.Start(runContext, binaryPath, logPath)
	if startError != nil {
		fmt.Fprintf(os.Stderr, "start scenario: %v\n", startError)

		return exitFailure
	}
	defer toxiproxyServer.Stop()

	readinessError := toxiproxyServer.WaitUntilReady(runContext)
	if readinessError != nil {
		if runContext.Err() != nil {
			fmt.Fprintln(os.Stdout, "Scenario startup cancelled.")

			return exitSuccess
		}

		fmt.Fprintf(os.Stderr, "start scenario: %v\n", readinessError)

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Toxiproxy is ready at %s.\n", toxiproxy.APIURL)
	fmt.Fprintf(os.Stdout, "Logs: %s\n", logPath)
	fmt.Fprintln(
		os.Stdout,
		"Elasticsearch, CCS, fixtures, and faults are not started in this milestone.",
	)
	fmt.Fprintln(os.Stdout, "Press Ctrl+C to stop Toxiproxy.")

	waitError := toxiproxyServer.Wait()
	if runContext.Err() != nil {
		fmt.Fprintln(os.Stdout, "Toxiproxy stopped.")

		return exitSuccess
	}

	if waitError != nil {
		fmt.Fprintf(os.Stderr, "Toxiproxy exited unexpectedly: %v\n", waitError)

		return exitFailure
	}

	fmt.Fprintln(os.Stderr, "Toxiproxy exited unexpectedly.")

	return exitFailure
}
