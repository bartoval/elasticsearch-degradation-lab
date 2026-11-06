package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlDirectoryIsShortAndIndependentOfWorkingDirectory(testContext *testing.T) {
	expectedDirectory := scenarioControlDirectory()
	deepDirectory := filepath.Join(testContext.TempDir(), strings.Repeat("deep", 35))
	if createError := os.MkdirAll(deepDirectory, 0o700); createError != nil {
		testContext.Fatal(createError)
	}
	testContext.Chdir(deepDirectory)

	if directory := scenarioControlDirectory(); directory != expectedDirectory {
		testContext.Fatalf("control directory changed with working directory: %q, want %q", directory, expectedDirectory)
	}

	if socketPath := filepath.Join(expectedDirectory, controlSocketName); len(socketPath) >= 104 {
		testContext.Fatalf("socket path exceeds the macOS limit: %q", socketPath)
	}
}

func TestControlDirectoryRejectsSymlinks(testContext *testing.T) {
	directory := filepath.Join(testContext.TempDir(), "control")
	if linkError := os.Symlink(testContext.TempDir(), directory); linkError != nil {
		testContext.Fatal(linkError)
	}

	if directoryError := ensureControlDirectory(directory); directoryError == nil {
		testContext.Fatal("accepted a symlink as the private control directory")
	}
}

func TestInactiveScenarioReportsFailureAndLogAfterCleanup(testContext *testing.T) {
	directory := shortRuntimeDirectory(testContext)
	lock, lockError := acquireScenarioLock(directory)
	if lockError != nil {
		testContext.Fatal(lockError)
	}

	state := scenarioState{ScenarioID: "local_test", LogPath: "/project/.runtime/local_test/logs/runner.log"}
	control, controlError := openScenarioControl(directory, state, lock, func() {})
	if controlError != nil {
		_ = lock.Close()
		testContext.Fatal(controlError)
	}

	if closeError := control.close(errors.New("coordinator exited unexpectedly")); closeError != nil {
		testContext.Fatal(closeError)
	}

	stateError := inactiveScenarioError(directory, "local_test")
	if stateError == nil || !strings.Contains(stateError.Error(), "coordinator exited unexpectedly") ||
		!strings.Contains(stateError.Error(), state.LogPath) {
		testContext.Fatalf("expected persisted failure with log, got %v", stateError)
	}
}

func TestInactiveScenarioReportsRunnerDisappearedWithoutCleanup(testContext *testing.T) {
	directory := shortRuntimeDirectory(testContext)
	state := scenarioState{ScenarioID: "local_test", LogPath: "/project/runner.log", Running: true}
	if stateError := writeScenarioState(directory, state); stateError != nil {
		testContext.Fatal(stateError)
	}

	stateError := inactiveScenarioError(directory, "")
	if stateError == nil || !strings.Contains(stateError.Error(), "without completing cleanup") {
		testContext.Fatalf("expected incomplete cleanup diagnostic, got %v", stateError)
	}
}
