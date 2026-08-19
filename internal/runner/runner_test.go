package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupSessionElasticsearchPreservesOperatorLogs(t *testing.T) {
	t.Parallel()
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

func TestOptionsWithDefaultsProvidesProgressReporter(t *testing.T) {
	t.Parallel()

	options := (Options{}).withDefaults()
	options.ReportProgress("ignored")
}

func TestManagedComponentBroadcastsUnexpectedExit(t *testing.T) {
	buildContext, cancelBuild := context.WithCancelCause(context.Background())
	environment := &Environment{
		buildCancel: cancelBuild,
		failureDone: make(chan struct{}),
		stopping:    make(chan struct{}),
	}
	done := make(chan struct{})
	processError := errors.New("process failed")
	environment.registerComponent(managedComponent{
		name:    "test process",
		logPath: "/tmp/test.log",
		done:    done,
		wait: func() error {
			return processError
		},
		stop: func() error { return nil },
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

func TestStopRemovesElasticsearchRuntimeAfterSuccessfulComponents(t *testing.T) {
	t.Parallel()

	sessionDirectory := filepath.Join(t.TempDir(), "local_test")
	elasticsearchDirectory := filepath.Join(sessionDirectory, "elasticsearch")
	if createError := os.MkdirAll(elasticsearchDirectory, 0o755); createError != nil {
		t.Fatalf("create Elasticsearch runtime: %v", createError)
	}

	_, cancelBuild := context.WithCancelCause(context.Background())
	environment := &Environment{
		components: []managedComponent{{
			name: "component",
			stop: func() error { return nil },
		}},
		sessionDirectory: sessionDirectory,
		buildCancel:      cancelBuild,
		stopping:         make(chan struct{}),
	}

	if stopError := environment.Stop(); stopError != nil {
		t.Fatalf("stop environment: %v", stopError)
	}
	if _, statError := os.Stat(elasticsearchDirectory); !os.IsNotExist(statError) {
		t.Fatalf("expected Elasticsearch runtime to be removed, got %v", statError)
	}
}

func TestStopUsesReverseStartupOrderAndPreservesRuntimeOnFailure(t *testing.T) {
	t.Parallel()

	sessionDirectory := filepath.Join(t.TempDir(), "local_test")
	elasticsearchFile := filepath.Join(sessionDirectory, "elasticsearch", "data")
	if createError := os.MkdirAll(elasticsearchFile, 0o755); createError != nil {
		t.Fatalf("create Elasticsearch runtime: %v", createError)
	}

	coordinatorError := errors.New("coordinator stop failed")
	remoteError := errors.New("remote stop failed")
	var stopped []string
	components := []managedComponent{
		{name: "Toxiproxy", stop: func() error { stopped = append(stopped, "Toxiproxy"); return nil }},
		{name: "remote Elasticsearch", stop: func() error { stopped = append(stopped, "remote"); return remoteError }},
		{name: "coordinator Elasticsearch", stop: func() error { stopped = append(stopped, "coordinator"); return coordinatorError }},
	}

	_, cancelBuild := context.WithCancelCause(context.Background())
	environment := &Environment{
		components:       components,
		sessionDirectory: sessionDirectory,
		buildCancel:      cancelBuild,
		stopping:         make(chan struct{}),
	}

	stopError := environment.Stop()
	if !errors.Is(stopError, coordinatorError) || !errors.Is(stopError, remoteError) {
		t.Fatalf("stop error = %v, want both component failures", stopError)
	}
	if actual := strings.Join(stopped, ","); actual != "coordinator,remote,Toxiproxy" {
		t.Fatalf("stop order = %s, want coordinator,remote,Toxiproxy", actual)
	}
	if _, statError := os.Stat(elasticsearchFile); statError != nil {
		t.Fatalf("failed stop removed Elasticsearch runtime: %v", statError)
	}

	if repeatedError := environment.Stop(); repeatedError != stopError {
		t.Fatalf("repeated stop error = %v, want cached %v", repeatedError, stopError)
	}
	if len(stopped) != len(components) {
		t.Fatalf("repeated stop invoked components again: %v", stopped)
	}
}
