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
	"testing"
	"time"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/runner"
	"github.com/bartoval/elasticsearch-degradation-lab/internal/scenario"
)

const backgroundTestRuntime = "KIBANA_SCENARIO_RUNNER_TEST_RUNTIME"

func TestBackgroundReadyHelper(testContext *testing.T) {
	runtimeDirectory := os.Getenv(backgroundTestRuntime)
	if runtimeDirectory == "" {
		return
	}

	ctx, cancelRun := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancelRun()
	control, readyFile, controlError := startCommandControl(runtimeDirectory, scenarioState{ScenarioID: "local_test"}, cancelRun)
	if controlError != nil {
		testContext.Fatalf("start helper control: %v", controlError)
	}

	defer readyFile.Close()

	if readyError := notifyBackgroundReady(readyFile); readyError != nil {
		_ = control.close(readyError)
		testContext.Fatalf("notify helper readiness: %v", readyError)
	}

	<-ctx.Done()
	writeError := os.WriteFile(filepath.Join(runtimeDirectory, "cleaned"), []byte("stopped"), 0o600)
	if closeError := control.close(writeError); closeError != nil {
		testContext.Fatalf("close helper control: %v", closeError)
	}
}

func TestBackgroundFailureHelper(testContext *testing.T) {
	if os.Getenv(backgroundTestRuntime) == "" {
		return
	}

	fmt.Fprintln(os.Stderr, "archive directory is missing")
	os.Exit(7)
}

func TestBackgroundStartingHelper(testContext *testing.T) {
	runtimeDirectory := os.Getenv(backgroundTestRuntime)
	if runtimeDirectory == "" {
		return
	}

	ctx, cancelRun := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancelRun()
	control, readyFile, controlError := startCommandControl(runtimeDirectory, scenarioState{ScenarioID: "local_test"}, cancelRun)
	if controlError != nil {
		testContext.Fatalf("start helper control: %v", controlError)
	}
	defer readyFile.Close()

	<-ctx.Done()
	if closeError := control.close(nil); closeError != nil {
		testContext.Fatalf("close helper control: %v", closeError)
	}
}

func TestBackgroundRunnerSurvivesLauncherAndStopsAfterCleanup(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	testContext.Cleanup(func() {
		ctx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelStop()
		_, _ = requestScenarioStop(ctx, runtimeDirectory, "local_test")
	})
	testContext.Setenv(backgroundTestRuntime, runtimeDirectory)
	options := runner.Options{
		RuntimeDirectory: filepath.Join(testContext.TempDir(), strings.Repeat("deep", 35)),
		Scenario:         scenario.Scenario{ID: "local_test"},
	}
	ctx, cancelLaunch := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLaunch()
	logPath, launchError := launchBackground(ctx, os.Args[0], []string{"-test.run=^TestBackgroundReadyHelper$"}, options, runtimeDirectory)
	if launchError != nil {
		testContext.Fatalf("launch helper: %v; log: %s", launchError, logPath)
	}

	_, duplicateError := acquireScenarioLock(runtimeDirectory)
	if duplicateError == nil {
		testContext.Fatal("background runner lost its runtime lock after launcher returned")
	}

	stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	stoppedID, stopError := requestScenarioStop(stopContext, runtimeDirectory, "local_test")
	if stopError != nil || stoppedID != "local_test" {
		testContext.Fatalf("stop helper: id=%q error=%v", stoppedID, stopError)
	}

	if stateError := inactiveScenarioError(runtimeDirectory, ""); stateError != nil {
		testContext.Fatalf("clean shutdown reported as failure: %v", stateError)
	}

	contents, readError := os.ReadFile(filepath.Join(runtimeDirectory, "cleaned"))
	if readError != nil || string(contents) != "stopped" {
		testContext.Fatalf("stop returned before cleanup: contents=%q error=%v", contents, readError)
	}

	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("stop returned before releasing the runtime: %v", lockError)
	}
	_ = lock.Close()
}

func TestBackgroundStartupFailureReleasesRuntime(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	testContext.Setenv(backgroundTestRuntime, runtimeDirectory)
	options := runner.Options{
		RuntimeDirectory: runtimeDirectory,
		Scenario:         scenario.Scenario{ID: "local_test"},
	}
	ctx, cancelLaunch := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLaunch()
	_, launchError := launchBackground(ctx, os.Args[0], []string{"-test.run=^TestBackgroundFailureHelper$"}, options, runtimeDirectory)
	if launchError == nil || !strings.Contains(launchError.Error(), "exit status 7") ||
		!strings.Contains(launchError.Error(), "archive directory is missing") {
		testContext.Fatalf("expected background startup failure, got %v", launchError)
	}

	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("failed startup did not release the runtime: %v", lockError)
	}
	_ = lock.Close()
}

func TestBackgroundStartupCancellationReleasesRuntime(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	testContext.Setenv(backgroundTestRuntime, runtimeDirectory)
	options := runner.Options{
		RuntimeDirectory: runtimeDirectory,
		Scenario:         scenario.Scenario{ID: "local_test"},
	}
	ctx, cancelLaunch := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelLaunch()
	_, launchError := launchBackground(ctx, os.Args[0], []string{"-test.run=^TestBackgroundStartingHelper$"}, options, runtimeDirectory)
	if !errors.Is(launchError, context.DeadlineExceeded) {
		testContext.Fatalf("expected startup cancellation, got %v", launchError)
	}

	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("cancelled startup did not release the runtime: %v", lockError)
	}
	_ = lock.Close()
}
