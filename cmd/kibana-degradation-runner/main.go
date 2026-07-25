package main

import (
	"fmt"
	"os"

	"kibana-degradation-runner/internal/scenario"
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
			return validateScenario(scenarioPath)
		}

		fmt.Fprintf(os.Stderr, "%s: not implemented yet\n", command)

		return exitFailure
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		fmt.Fprintln(os.Stderr, usage)

		return exitInvalidArguments
	}
}

// validateScenario reports configuration problems before any infrastructure is started.
func validateScenario(scenarioPath string) int {
	loadedScenario, err := scenario.Load(scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario validation failed: %v\n", err)

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Scenario %q is valid.\n", loadedScenario.ID)

	return exitSuccess
}
