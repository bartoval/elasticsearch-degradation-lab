package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestScenarioLockPreventsConcurrentStarts(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("acquire scenario lock: %v", lockError)
	}
	defer lock.Close()

	_, secondError := acquireScenarioLock(runtimeDirectory)
	if secondError == nil || !strings.Contains(secondError.Error(), "another scenario is already running") {
		testContext.Fatalf("expected duplicate start error, got %v", secondError)
	}
}

func TestScenarioStopWaitsForCleanupAndReportsFailure(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("acquire scenario lock: %v", lockError)
	}

	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	control, controlError := openScenarioControl(runtimeDirectory, scenarioState{ScenarioID: "local_test", LogPath: "/project/.runtime/local_test/logs/runner.log"}, lock, cancelRun)
	if controlError != nil {
		_ = lock.Close()
		testContext.Fatalf("open control: %v", controlError)
	}
	defer control.close(nil)

	cleanupFailure := errors.New("coordinator cleanup failed")
	closed := make(chan error, 1)
	go func() {
		<-runContext.Done()
		closed <- control.close(cleanupFailure)
	}()

	stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	_, stopError := requestScenarioStop(stopContext, runtimeDirectory, "local_test")
	if stopError == nil || !strings.Contains(stopError.Error(), cleanupFailure.Error()) {
		testContext.Fatalf("expected cleanup failure, got %v", stopError)
	}

	if closeError := <-closed; closeError != nil {
		testContext.Fatalf("close control: %v", closeError)
	}

	replacement, replacementError := acquireScenarioLock(runtimeDirectory)
	if replacementError != nil {
		testContext.Fatalf("runtime was not released after cleanup: %v", replacementError)
	}
	_ = replacement.Close()
}

func TestScenarioStopRejectsDifferentScenario(testContext *testing.T) {
	runtimeDirectory := shortRuntimeDirectory(testContext)
	lock, lockError := acquireScenarioLock(runtimeDirectory)
	if lockError != nil {
		testContext.Fatalf("acquire scenario lock: %v", lockError)
	}

	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	control, controlError := openScenarioControl(runtimeDirectory, scenarioState{ScenarioID: "local_test", LogPath: "/project/.runtime/local_test/logs/runner.log"}, lock, cancelRun)
	if controlError != nil {
		_ = lock.Close()
		testContext.Fatalf("open control: %v", controlError)
	}
	defer control.close(nil)

	stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	_, stopError := requestScenarioStop(stopContext, runtimeDirectory, "another_scenario")
	if stopError == nil || !strings.Contains(stopError.Error(), `scenario "local_test" is running`) {
		testContext.Fatalf("expected scenario mismatch error, got %v", stopError)
	}

	if runContext.Err() != nil {
		testContext.Fatal("a different scenario was stopped")
	}
}

func TestScenarioStopWithoutActiveRunner(testContext *testing.T) {
	_, stopError := requestScenarioStop(context.Background(), shortRuntimeDirectory(testContext), "")
	if !errors.Is(stopError, errNoActiveScenario) {
		testContext.Fatalf("expected no active scenario, got %v", stopError)
	}
}

func TestScenarioStopTimeoutLeavesCleanupRunningAndShowsLog(testContext *testing.T) {
	directory := shortRuntimeDirectory(testContext)
	lock, lockError := acquireScenarioLock(directory)
	if lockError != nil {
		testContext.Fatal(lockError)
	}

	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	state := scenarioState{ScenarioID: "local_test", LogPath: "/project/runner.log"}
	control, controlError := openScenarioControl(directory, state, lock, cancelRun)
	if controlError != nil {
		_ = lock.Close()
		testContext.Fatal(controlError)
	}
	defer control.close(nil)

	stopContext, cancelStop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelStop()
	_, stopError := requestScenarioStop(stopContext, directory, "")
	if !errors.Is(stopError, context.DeadlineExceeded) || runContext.Err() == nil {
		testContext.Fatalf("stop did not request cleanup before timing out: %v", stopError)
	}

	diagnostic := describeStopError(directory, stopError)
	if !strings.Contains(diagnostic, "shutdown continues in the background") || !strings.Contains(diagnostic, state.LogPath) {
		testContext.Fatalf("missing cleanup status or log: %s", diagnostic)
	}
}

func shortRuntimeDirectory(testContext *testing.T) string {
	testContext.Helper()

	directory, createError := os.MkdirTemp("", "ksr-")
	if createError != nil {
		testContext.Fatalf("create runtime directory: %v", createError)
	}
	testContext.Cleanup(func() { _ = os.RemoveAll(directory) })

	return directory
}
