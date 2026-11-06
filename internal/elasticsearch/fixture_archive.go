package elasticsearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/process"
)

const archiveLoaderScript = `
async function loadArchive() {
  require('@kbn/setup-node-env');

  const { Client } = require('@elastic/elasticsearch');
  const { EsArchiver } = require('@kbn/es-archiver');
  const { ToolingLog } = require('@kbn/tooling-log');
  const [mode, targetURL, archivePath] = process.argv.slice(1);
  const username = process.env.DEGRADATION_ARCHIVE_USERNAME;
  const password = process.env.DEGRADATION_ARCHIVE_PASSWORD;
  const client = new Client({ node: targetURL, auth: { username, password } });

  try {
    const log = new ToolingLog({ level: 'info', writeTo: process.stdout });
    const capability = { dataOnlyRead: false };
    const archiver = new EsArchiver({
      client,
      log,
      baseDir: process.cwd(),
      get dataOnly() {
        capability.dataOnlyRead = true;

        return true;
      },
    });

    if (!capability.dataOnlyRead) {
      throw new Error('This Kibana checkout does not support EsArchiver dataOnly mode');
    }

    if (mode === 'check') {
      return;
    }

    await archiver.load(archivePath);
  } finally {
    await client.close();
  }
}

function reportFailure(error) {
  console.error(error);
  process.exit(1);
}

function reportSuccess() {
  process.exit(0);
}

loadArchive().then(reportSuccess, reportFailure);
`

// CheckArchive verifies the archive directory, Node.js, and Kibana API without loading data.
func CheckArchive(ctx context.Context, kibanaDirectory string, archivePath string) error {
	spec, specError := archiveSpec(kibanaDirectory, archivePath, "check", "http://127.0.0.1:0")
	if specError != nil {
		return specError
	}

	var output bytes.Buffer
	spec.Output = &output
	if checkError := runArchive(ctx, spec); checkError != nil {
		if ctx.Err() != nil {
			return checkError
		}

		return fmt.Errorf(
			"check Kibana archive support: %w\n%s",
			checkError,
			output.String(),
		)
	}

	return nil
}

// ApplyArchive loads Elasticsearch data through the Kibana checkout's EsArchiver API.
func (node *Node) ApplyArchive(ctx context.Context, kibanaDirectory string, archivePath string, logPath string) error {
	spec, specError := archiveSpec(kibanaDirectory, archivePath, "load", node.url)
	if specError != nil {
		return specError
	}

	logFile, logError := createLogFile(logPath)
	if logError != nil {
		return logError
	}
	defer logFile.Close()

	spec.Output = logFile
	if loadError := runArchive(ctx, spec); loadError != nil {
		return fmt.Errorf(
			"load Elasticsearch archive %q: %w\nRecent log:\n%s\nFull log: %s",
			archivePath,
			loadError,
			process.RecentLog(logPath),
			logPath,
		)
	}

	return nil
}

func archiveSpec(kibanaDirectory string, archivePath string, mode string, targetURL string) (process.Spec, error) {
	archiveDirectory := archivePath
	if !filepath.IsAbs(archiveDirectory) {
		archiveDirectory = filepath.Join(kibanaDirectory, archiveDirectory)
	}

	archiveInfo, statError := os.Stat(archiveDirectory)
	if statError != nil {
		return process.Spec{}, fmt.Errorf("find Elasticsearch archive %q: %w", archiveDirectory, statError)
	}

	if !archiveInfo.IsDir() {
		return process.Spec{}, fmt.Errorf("Elasticsearch archive %q must be a directory", archiveDirectory)
	}

	nodePath, nodeError := exec.LookPath("node")
	if nodeError != nil {
		return process.Spec{}, fmt.Errorf("find node in PATH: %w", nodeError)
	}

	return process.Spec{
		Path: nodePath,
		Arguments: []string{
			"--eval", archiveLoaderScript, "--",
			mode, targetURL, archiveDirectory,
		},
		Directory: kibanaDirectory,
		Environment: append(
			os.Environ(),
			"DEGRADATION_ARCHIVE_USERNAME="+elasticsearchUsername,
			"DEGRADATION_ARCHIVE_PASSWORD="+elasticsearchPassword,
		),
		ProcessGroup:  true,
		ShutdownGrace: shutdownGrace,
	}, nil
}

func runArchive(ctx context.Context, spec process.Spec) error {
	child, startError := process.Start(ctx, spec)
	if startError != nil {
		return fmt.Errorf("start Elasticsearch archive loader: %w", startError)
	}

	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), child.Stop())
	case <-child.Done():
	}

	return errors.Join(child.Wait(), child.Stop())
}
