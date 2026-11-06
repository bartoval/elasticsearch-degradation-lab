package process

import (
	"io"
	"os"
	"strings"
)

const recentLogBytes = 4 * 1024

// RecentLog reads the last four KiB of process output for failure diagnostics.
func RecentLog(logPath string) string {
	logFile, openError := os.Open(logPath)
	if openError != nil {
		return "unable to read log: " + openError.Error()
	}
	defer logFile.Close()

	info, statError := logFile.Stat()
	if statError != nil {
		return "unable to read log: " + statError.Error()
	}

	offset := max(0, info.Size()-recentLogBytes)
	contents := make([]byte, min(info.Size(), recentLogBytes))
	readCount, readError := logFile.ReadAt(contents, offset)
	if readError != nil && readError != io.EOF {
		return "unable to read log: " + readError.Error()
	}

	return strings.TrimSpace(string(contents[:readCount]))
}
