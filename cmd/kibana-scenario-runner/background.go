package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/process"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/runner"
)

const (
	backgroundEnvironment = "KIBANA_SCENARIO_RUNNER_BACKGROUND"
	backgroundReadyToken  = "ready\n"
	backgroundReadyFD     = 3
	backgroundLockFD      = 4
)

type stopWaitError struct {
	Err error
}

func (waitError *stopWaitError) Error() string {
	return "stopped waiting for scenario cleanup: " + waitError.Err.Error()
}

func (waitError *stopWaitError) Unwrap() error {
	return waitError.Err
}

func runPlayCommand(scenarioReference string, kibanaDirectory string) int {
	options, optionsError := resolveStartOptions(scenarioReference, kibanaDirectory)
	if optionsError != nil {
		fmt.Fprintln(os.Stderr, optionsError)

		return exitFailure
	}

	executable, executableError := os.Executable()
	if executableError != nil {
		fmt.Fprintf(os.Stderr, "find runner executable: %v\n", executableError)

		return exitFailure
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stopRestore := context.AfterFunc(ctx, stopSignals)
	defer stopRestore()

	fmt.Fprintf(os.Stdout, "Starting scenario %q in the background.\n", options.Scenario.ID)
	fmt.Fprintf(os.Stdout, "Progress log: %s\n", backgroundLogPath(options))
	logPath, launchError := launchBackground(ctx, executable, []string{
		"up", scenarioReference, kibanaDirectoryOption, options.KibanaDirectory,
	}, options, scenarioControlDirectory())
	if launchError != nil {
		return reportStartupFailure(ctx, launchError)
	}

	fmt.Fprintf(os.Stdout, "Scenario %q infrastructure is ready in the background.\n", options.Scenario.Name)
	fmt.Fprintf(os.Stdout, "Runner log: %s\n", logPath)
	fmt.Fprintln(os.Stdout, "Start Kibana separately. Run kibana-scenario-runner stop to stop the environment.")

	return exitSuccess
}

func runStopCommand(scenarioReference string) int {
	scenarioID, referenceError := stopScenarioID(scenarioReference)
	if referenceError != nil {
		fmt.Fprintln(os.Stderr, referenceError)

		return exitFailure
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancelStop := context.WithTimeout(signalContext, stopTimeout)
	defer cancelStop()

	controlDirectory := scenarioControlDirectory()
	stoppedID, stopError := requestScenarioStop(ctx, controlDirectory, scenarioID)
	if errors.Is(stopError, errNoActiveScenario) {
		if stateError := inactiveScenarioError(controlDirectory, scenarioID); stateError != nil {
			fmt.Fprintln(os.Stderr, stateError)

			return exitFailure
		}

		fmt.Fprintln(os.Stdout, "No scenario is running.")

		return exitSuccess
	}

	if stopError != nil {
		fmt.Fprintln(os.Stderr, describeStopError(controlDirectory, stopError))

		return exitFailure
	}

	fmt.Fprintf(os.Stdout, "Scenario %q stopped.\n", stoppedID)

	return exitSuccess
}

func launchBackground(
	ctx context.Context,
	executable string,
	arguments []string,
	options runner.Options,
	controlDirectory string,
) (string, error) {
	if contextError := ctx.Err(); contextError != nil {
		return "", contextError
	}

	lock, lockError := acquireScenarioLock(controlDirectory)
	if lockError != nil {
		return "", lockError
	}
	defer func() {
		if lock != nil {
			_ = lock.Close()
		}
	}()

	logPath := backgroundLogPath(options)
	if directoryError := os.MkdirAll(filepath.Dir(logPath), 0o755); directoryError != nil {
		return logPath, fmt.Errorf("create runner log directory: %w", directoryError)
	}

	logFile, logError := os.Create(logPath)
	if logError != nil {
		return logPath, fmt.Errorf("create runner log: %w", logError)
	}
	defer logFile.Close()

	readyReader, readyWriter, pipeError := os.Pipe()
	if pipeError != nil {
		return logPath, fmt.Errorf("create runner readiness pipe: %w", pipeError)
	}
	defer readyReader.Close()

	command := exec.Command(executable, arguments...)
	command.Env = append(os.Environ(), backgroundEnvironment+"=1")
	command.Stdout = logFile
	command.Stderr = logFile
	command.ExtraFiles = []*os.File{readyWriter, lock}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if startError := command.Start(); startError != nil {
		_ = readyWriter.Close()

		return logPath, fmt.Errorf("start background runner: %w", startError)
	}
	_ = readyWriter.Close()
	_ = lock.Close()
	lock = nil

	ready := make(chan error, 1)
	go readBackgroundReady(readyReader, ready)

	select {
	case <-ctx.Done():
		signalError := command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()

		if errors.Is(signalError, os.ErrProcessDone) {
			signalError = nil
		}

		if signalError != nil {
			return logPath, errors.Join(ctx.Err(), signalError)
		}

		return logPath, ctx.Err()
	case readyError := <-ready:
		if readyError != nil {
			waitError := command.Wait()

			return logPath, fmt.Errorf(
				"background runner exited before becoming ready: %w\nRunner log: %s\n%s",
				errors.Join(readyError, waitError),
				logPath,
				process.RecentLog(logPath),
			)
		}
	}

	if releaseError := command.Process.Release(); releaseError != nil {
		return logPath, fmt.Errorf("release background runner: %w", releaseError)
	}

	return logPath, nil
}

func startCommandControl(
	controlDirectory string,
	state scenarioState,
	cancel context.CancelFunc,
) (*scenarioControl, *os.File, error) {
	readyFile, lock, filesError := commandControlFiles(controlDirectory)
	if filesError != nil {
		return nil, nil, filesError
	}

	control, controlError := openScenarioControl(controlDirectory, state, lock, cancel)
	if controlError != nil {
		_ = lock.Close()
		if readyFile != nil {
			_ = readyFile.Close()
		}

		return nil, nil, controlError
	}

	return control, readyFile, nil
}

func commandControlFiles(controlDirectory string) (*os.File, *os.File, error) {
	if os.Getenv(backgroundEnvironment) == "1" {
		_ = os.Unsetenv(backgroundEnvironment)
		syscall.CloseOnExec(backgroundReadyFD)
		syscall.CloseOnExec(backgroundLockFD)

		return os.NewFile(backgroundReadyFD, "runner-readiness"), os.NewFile(backgroundLockFD, "runner-lock"), nil
	}

	lock, lockError := acquireScenarioLock(controlDirectory)

	return nil, lock, lockError
}

func notifyBackgroundReady(readyFile *os.File) error {
	if readyFile == nil {
		return nil
	}

	_, writeError := readyFile.WriteString(backgroundReadyToken)

	return writeError
}

func readBackgroundReady(reader *os.File, ready chan<- error) {
	contents := make([]byte, len(backgroundReadyToken))
	_, readError := io.ReadFull(reader, contents)
	if readError != nil {
		ready <- readError

		return
	}

	if string(contents) != backgroundReadyToken {
		ready <- errors.New("unexpected runner readiness response")

		return
	}

	ready <- nil
}

func backgroundLogPath(options runner.Options) string {
	return filepath.Join(options.RuntimeDirectory, options.Scenario.ID, "logs", "runner.log")
}

func stopScenarioID(reference string) (string, error) {
	if reference == "" {
		return "", nil
	}

	loadedScenario, loadError := loadScenario(reference)
	if loadError != nil {
		return "", loadError
	}

	return loadedScenario.ID, nil
}

func describeStopError(controlDirectory string, stopError error) string {
	var waitError *stopWaitError
	if errors.As(stopError, &waitError) {
		message := "Stopped waiting for cleanup; shutdown continues in the background."
		if errors.Is(waitError, context.DeadlineExceeded) {
			message = "Timed out waiting for cleanup; shutdown continues in the background."
		}

		state, stateError := readScenarioState(controlDirectory)
		if stateError == nil {
			return fmt.Sprintf("%s\nLogs: %s", message, state.LogPath)
		}

		return message
	}

	return fmt.Sprintf("stop scenario: %v", stopError)
}
