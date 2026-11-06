package elasticsearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckArchiveDoesNotLoadData(testContext *testing.T) {
	kibanaDirectory, archivePath, _ := prepareArchiveCheckout(testContext)
	if writeError := os.WriteFile(filepath.Join(archivePath, "fail"), nil, 0o600); writeError != nil {
		testContext.Fatalf("write failure marker: %v", writeError)
	}

	if checkError := CheckArchive(context.Background(), kibanaDirectory, archivePath); checkError != nil {
		testContext.Fatalf("check archive without loading: %v", checkError)
	}
}

func TestCheckArchiveRejectsMissingDirectory(testContext *testing.T) {
	checkError := CheckArchive(context.Background(), testContext.TempDir(), "missing-archive")
	if !errors.Is(checkError, os.ErrNotExist) {
		testContext.Fatalf("expected missing directory error, got %v", checkError)
	}
}

func TestCheckArchiveRejectsMissingNode(testContext *testing.T) {
	testContext.Setenv("PATH", "")
	archivePath := testContext.TempDir()
	checkError := CheckArchive(context.Background(), archivePath, archivePath)
	if checkError == nil || !strings.Contains(checkError.Error(), "find node in PATH") {
		testContext.Fatalf("expected missing Node.js error, got %v", checkError)
	}
}

func TestCheckArchiveRejectsUnsupportedDataOnlyOption(testContext *testing.T) {
	kibanaDirectory, archivePath, _ := prepareArchiveCheckout(testContext)
	modulePath := filepath.Join(kibanaDirectory, "node_modules", "@kbn", "es-archiver", "index.js")
	if writeError := os.WriteFile(modulePath, []byte("exports.EsArchiver = class {};"), 0o600); writeError != nil {
		testContext.Fatalf("replace archive API: %v", writeError)
	}

	checkError := CheckArchive(context.Background(), kibanaDirectory, archivePath)
	if checkError == nil || !strings.Contains(checkError.Error(), "does not support EsArchiver dataOnly mode") {
		testContext.Fatalf("expected unsupported API error, got %v", checkError)
	}
}

func TestApplyArchiveUsesDataOnlyAPIAndDirectEndpoint(testContext *testing.T) {
	kibanaDirectory, archivePath, logPath := prepareArchiveCheckout(testContext)
	node := &Node{url: "http://127.0.0.1:9201"}

	if loadError := node.ApplyArchive(context.Background(), kibanaDirectory, "archive with spaces", logPath); loadError != nil {
		testContext.Fatalf("load archive: %v", loadError)
	}

	spec, specError := archiveSpec(kibanaDirectory, archivePath, "load", node.url)
	if specError != nil {
		testContext.Fatalf("prepare archive command: %v", specError)
	}

	for _, argument := range spec.Arguments {
		if argument == elasticsearchUsername || argument == elasticsearchPassword {
			testContext.Fatal("archive command exposes credentials in its arguments")
		}
	}

	contents, readError := os.ReadFile(logPath)
	if readError != nil {
		testContext.Fatalf("read archive log: %v", readError)
	}

	for _, expected := range []string{archivePath, kibanaDirectory, node.url, `"dataOnly":true`, `"username":"elastic"`, "client closed"} {
		if !strings.Contains(string(contents), expected) {
			testContext.Fatalf("archive log %q does not contain %q", contents, expected)
		}
	}
}

func TestApplyArchiveReportsLoaderFailureAndClosesClient(testContext *testing.T) {
	kibanaDirectory, archivePath, logPath := prepareArchiveCheckout(testContext)
	if writeError := os.WriteFile(filepath.Join(archivePath, "fail"), nil, 0o600); writeError != nil {
		testContext.Fatalf("write failure marker: %v", writeError)
	}

	node := &Node{url: "http://127.0.0.1:9210"}
	ctx, cancelLoad := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLoad()

	loadError := node.ApplyArchive(ctx, kibanaDirectory, archivePath, logPath)
	if loadError == nil {
		testContext.Fatal("expected archive failure")
	}

	for _, expected := range []string{"archive rejected", "client closed", logPath} {
		if !strings.Contains(loadError.Error(), expected) {
			testContext.Fatalf("loader error %q does not contain %q", loadError, expected)
		}
	}
}

func prepareArchiveCheckout(testContext *testing.T) (string, string, string) {
	testContext.Helper()

	kibanaDirectory := testContext.TempDir()
	archivePath := filepath.Join(kibanaDirectory, "archive with spaces")
	if createError := os.MkdirAll(archivePath, 0o755); createError != nil {
		testContext.Fatalf("create archive: %v", createError)
	}

	modules := map[string]string{
		"@kbn/setup-node-env": "",
		"@kbn/tooling-log":    "exports.ToolingLog = class {};",
		"@elastic/elasticsearch": `exports.Client = class {
  constructor(options) { this.options = options; }
  async close() { console.log('client closed'); }
};`,
		"@kbn/es-archiver": `const fs = require('node:fs');
function keepAlive() {}

exports.EsArchiver = class {
  constructor(options) {
    this.options = { ...options };
  }
  async load(archivePath) {
    if (fs.existsSync(archivePath + '/fail')) {
      setInterval(keepAlive, 1000);
      throw new Error('archive rejected');
    }

    console.log(JSON.stringify({
      archivePath,
      baseDir: this.options.baseDir,
      dataOnly: this.options.dataOnly,
      client: this.options.client.options,
    }));
  }
};`,
	}

	for modulePath, contents := range modules {
		moduleDirectory := filepath.Join(kibanaDirectory, "node_modules", modulePath)
		if createError := os.MkdirAll(moduleDirectory, 0o755); createError != nil {
			testContext.Fatalf("create module: %v", createError)
		}

		if writeError := os.WriteFile(filepath.Join(moduleDirectory, "index.js"), []byte(contents), 0o600); writeError != nil {
			testContext.Fatalf("write module: %v", writeError)
		}
	}

	return kibanaDirectory, archivePath, filepath.Join(testContext.TempDir(), "logs", "archive.log")
}
