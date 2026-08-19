package process

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	helperProcessEnvironment = "DEGRADATION_RUNNER_PROCESS_HELPER"
	helperModeEnvironment    = "DEGRADATION_RUNNER_PROCESS_MODE"
	helperReadyEnvironment   = "DEGRADATION_RUNNER_PROCESS_READY"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv(helperProcessEnvironment) != "1" {
		return
	}

	switch os.Getenv(helperModeEnvironment) {
	case "exit":
		os.Exit(7)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		if writeError := os.WriteFile(os.Getenv(helperReadyEnvironment), []byte("ready"), 0o600); writeError != nil {
			os.Exit(8)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(9)
	}
}

func helperSpec(mode string, readyPath string) Spec {
	environment := append(
		os.Environ(),
		helperProcessEnvironment+"=1",
		helperModeEnvironment+"="+mode,
	)
	if readyPath != "" {
		environment = append(environment, helperReadyEnvironment+"="+readyPath)
	}

	return Spec{
		Path:          os.Args[0],
		Arguments:     []string{"-test.run=^TestProcessHelper$"},
		Environment:   environment,
		ProcessGroup:  true,
		ShutdownGrace: 50 * time.Millisecond,
	}
}

func TestWaitBroadcastsCachedExitOutcome(t *testing.T) {
	child, startError := Start(context.Background(), helperSpec("exit", ""))
	if startError != nil {
		t.Fatalf("start helper: %v", startError)
	}

	select {
	case <-child.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for helper exit")
	}

	firstError := child.Wait()
	secondError := child.Wait()
	if firstError == nil || secondError == nil {
		t.Fatalf("expected both waiters to receive an exit error: first=%v second=%v", firstError, secondError)
	}
	if firstError.Error() != secondError.Error() {
		t.Fatalf("cached outcomes differ: first=%q second=%q", firstError, secondError)
	}
}

func TestStopEscalatesAndIsIdempotent(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	spec := helperSpec("ignore-term", readyPath)
	child, startError := Start(context.Background(), spec)
	if startError != nil {
		t.Fatalf("start helper: %v", startError)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statError := os.Stat(readyPath); statError == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Stop()
			t.Fatal("helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	startedAt := time.Now()
	firstError := child.Stop()
	elapsed := time.Since(startedAt)
	if firstError != nil {
		t.Fatalf("stop helper: %v", firstError)
	}
	if elapsed < spec.ShutdownGrace/2 {
		t.Fatalf("stop returned in %s before allowing the %s grace", elapsed, spec.ShutdownGrace)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("stop took too long: %s", elapsed)
	}

	if secondError := child.Stop(); secondError != nil {
		t.Fatalf("second stop returned an error: %v", secondError)
	}
	if waitError := child.Wait(); waitError == nil || !strings.Contains(waitError.Error(), "signal: killed") {
		t.Fatalf("expected SIGKILL exit outcome, got %v", waitError)
	}
}
