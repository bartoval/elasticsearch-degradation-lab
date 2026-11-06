package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecentLogReturnsBoundedTail(testContext *testing.T) {
	logPath := filepath.Join(testContext.TempDir(), "runner.log")
	contents := strings.Repeat("old output\n", recentLogBytes) + strings.Repeat("x", recentLogBytes-10) + "\nfailed\n"
	if writeError := os.WriteFile(logPath, []byte(contents), 0o600); writeError != nil {
		testContext.Fatal(writeError)
	}

	expected := strings.TrimSpace(contents[len(contents)-recentLogBytes:])
	if actual := RecentLog(logPath); actual != expected {
		testContext.Fatalf("unexpected log tail: length=%d, want %d", len(actual), len(expected))
	}
}

func TestRecentLogReportsMissingFile(testContext *testing.T) {
	actual := RecentLog(filepath.Join(testContext.TempDir(), "missing.log"))
	if !strings.Contains(actual, "unable to read log") {
		testContext.Fatalf("expected log read diagnostic, got %q", actual)
	}
}
