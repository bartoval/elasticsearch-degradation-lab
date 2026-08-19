package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupSessionElasticsearchPreservesOperatorLogs(t *testing.T) {
	sessionDirectory := filepath.Join(t.TempDir(), "local_test")
	elasticsearchFile := filepath.Join(
		sessionDirectory,
		"elasticsearch",
		"coordinator",
		"data",
		"fixture",
	)
	logFile := filepath.Join(sessionDirectory, "logs", "elasticsearch-coordinator.log")

	for _, path := range []string{elasticsearchFile, logFile} {
		if createError := os.MkdirAll(filepath.Dir(path), 0o755); createError != nil {
			t.Fatalf("create directory for %s: %v", path, createError)
		}
		if writeError := os.WriteFile(path, []byte("contents"), 0o600); writeError != nil {
			t.Fatalf("write %s: %v", path, writeError)
		}
	}

	if cleanupError := cleanupSessionElasticsearch(sessionDirectory); cleanupError != nil {
		t.Fatalf("cleanup runtime: %v", cleanupError)
	}

	if _, statError := os.Stat(filepath.Join(sessionDirectory, "elasticsearch")); !os.IsNotExist(statError) {
		t.Fatalf("expected Elasticsearch directory to be removed, got %v", statError)
	}
	if contents, readError := os.ReadFile(logFile); readError != nil || string(contents) != "contents" {
		t.Fatalf("operator log was not preserved: contents=%q error=%v", contents, readError)
	}
}

func TestMonitorProcessBroadcastsUnexpectedExit(t *testing.T) {
	buildContext, cancelBuild := context.WithCancelCause(context.Background())
	environment := &Environment{
		buildCancel: cancelBuild,
		failureDone: make(chan struct{}),
		stopping:    make(chan struct{}),
	}
	done := make(chan struct{})
	processError := errors.New("process failed")
	environment.monitorProcess("test process", "/tmp/test.log", done, func() error {
		return processError
	})
	close(done)

	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	firstError := environment.Wait(waitContext)
	secondError := environment.Wait(waitContext)
	for waiterIndex, waitError := range []error{firstError, secondError} {
		var exitError *UnexpectedExitError
		if !errors.As(waitError, &exitError) || !errors.Is(waitError, processError) {
			t.Fatalf("waiter %d received %v, want cached unexpected exit", waiterIndex, waitError)
		}
	}

	var cause *UnexpectedExitError
	if !errors.As(context.Cause(buildContext), &cause) {
		t.Fatalf("build context cause = %v, want unexpected exit", context.Cause(buildContext))
	}
}
