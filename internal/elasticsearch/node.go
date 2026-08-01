package elasticsearch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	localHost = "127.0.0.1"

	// These credentials are the ones Kibana's own scripts/es.js configures in the
	// native realm, so they authenticate every request the runner makes.
	elasticsearchUsername = "elastic"
	elasticsearchPassword = "changeme"

	readinessTimeout        = 10 * time.Minute
	readinessInterval       = 1 * time.Second
	progressInterval        = 10 * time.Second
	readinessRequestTimeout = 2 * time.Second
	setupRequestTimeout     = 30 * time.Second
	shutdownTimeout         = 10 * time.Second
	recentLogBytes          = 4 * 1024

	// maxShardsPerNode has to cover the largest fan-out fixture the scenario
	// package allows, since every generated data stream costs one shard.
	maxShardsPerNode = 5000

	// elasticsearchJavaOpts raises the heap above the snapshot default, which a
	// fan-out fixture exhausts while answering _field_caps over its mappings.
	elasticsearchJavaOpts = "-Xms2g -Xmx2g"
)

var (
	readinessHTTPClient = &http.Client{Timeout: readinessRequestTimeout}
	setupHTTPClient     = &http.Client{Timeout: setupRequestTimeout}
)

// Options identifies one Elasticsearch node and its local ports.
type Options struct {
	ID            string
	HTTPPort      int
	TransportPort int
}

// Node contains only the state required to wait for and stop one local node.
type Node struct {
	id            string
	url           string
	logPath       string
	httpPort      int
	transportPort int
	cancelProcess context.CancelFunc
	done          chan error
}

// Start launches Kibana's existing Elasticsearch snapshot tool.
func Start(
	ctx context.Context,
	kibanaDirectory string,
	sessionDirectory string,
	options Options,
) (*Node, error) {
	scriptPath := filepath.Join(kibanaDirectory, "scripts", "es.js")
	if _, statError := os.Stat(scriptPath); statError != nil {
		return nil, fmt.Errorf("find Kibana ES tool %q: %w", scriptPath, statError)
	}

	nodePath, nodeError := exec.LookPath("node")
	if nodeError != nil {
		return nil, fmt.Errorf("find node in PATH: %w", nodeError)
	}

	if portError := assertPortAvailable(options.HTTPPort); portError != nil {
		return nil, portError
	}

	if portError := assertPortAvailable(options.TransportPort); portError != nil {
		return nil, portError
	}

	nodeDirectory := filepath.Join(sessionDirectory, "elasticsearch", options.ID)
	dataDirectory := filepath.Join(nodeDirectory, "data")
	internalLogDirectory := filepath.Join(nodeDirectory, "logs")

	directoryError := prepareNodeDirectory(
		kibanaDirectory,
		nodeDirectory,
		dataDirectory,
		internalLogDirectory,
	)
	if directoryError != nil {
		return nil, directoryError
	}

	logPath := filepath.Join(sessionDirectory, "logs", "elasticsearch-"+options.ID+".log")
	logFile, logError := createLogFile(logPath)
	if logError != nil {
		return nil, logError
	}
	defer logFile.Close()

	processContext, cancelProcess := context.WithCancel(ctx)
	command := exec.CommandContext(
		processContext,
		nodePath,
		snapshotArguments(
			scriptPath,
			nodeDirectory,
			dataDirectory,
			internalLogDirectory,
			options,
		)...,
	)
	command.Dir = kibanaDirectory
	// A fan-out fixture holds tens of thousands of field mappings in cluster
	// state, and _field_caps over them exhausts the small default heap.
	command.Env = append(os.Environ(), "ES_JAVA_OPTS="+elasticsearchJavaOpts)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = shutdownTimeout
	command.Cancel = func() error {
		signalError := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(signalError, syscall.ESRCH) {
			return nil
		}

		return signalError
	}

	if startError := command.Start(); startError != nil {
		cancelProcess()

		return nil, fmt.Errorf("start Elasticsearch %s: %w", options.ID, startError)
	}

	node := &Node{
		id:            options.ID,
		url:           "http://" + net.JoinHostPort(localHost, strconv.Itoa(options.HTTPPort)),
		logPath:       logPath,
		httpPort:      options.HTTPPort,
		transportPort: options.TransportPort,
		cancelProcess: cancelProcess,
		done:          make(chan error, 1),
	}

	go func() {
		node.done <- command.Wait()
		close(node.done)
	}()

	return node, nil
}

// WaitUntilReady reports the latest failure and launcher activity every ten seconds.
func (node *Node) WaitUntilReady(ctx context.Context, reportProgress func(string)) error {
	timeout := time.NewTimer(readinessTimeout)
	defer timeout.Stop()

	readinessTicker := time.NewTicker(readinessInterval)
	defer readinessTicker.Stop()

	progressTicker := time.NewTicker(progressInterval)
	defer progressTicker.Stop()

	startedAt := time.Now()
	var lastError error

	for {
		ready, readinessError := node.isReady(ctx)
		if ready {
			reportProgress(fmt.Sprintf("Elasticsearch %s is ready at %s", node.id, node.url))

			return nil
		}

		lastError = readinessError

		select {
		case <-ctx.Done():
			return ctx.Err()
		case processError := <-node.done:
			return fmt.Errorf(
				"Elasticsearch %s exited before becoming ready: %v\nRecent log:\n%s\nFull log: %s",
				node.id,
				processError,
				recentLog(node.logPath),
				node.logPath,
			)
		case <-timeout.C:
			return fmt.Errorf(
				"timed out waiting for Elasticsearch %s: %v\nFull log: %s",
				node.id,
				lastError,
				node.logPath,
			)
		case <-progressTicker.C:
			reportProgress(
				fmt.Sprintf(
					"Waiting for Elasticsearch %s (%s elapsed). Last error: %v. Latest log: %s",
					node.id,
					time.Since(startedAt).Round(time.Second),
					lastError,
					latestLogLine(node.logPath),
				),
			)
		case <-readinessTicker.C:
		}
	}
}

// URL returns the endpoint that Kibana should use for the coordinator.
func (node *Node) URL() string {
	return node.url
}

// LogPath points to the complete output from Kibana's ES tool.
func (node *Node) LogPath() string {
	return node.logPath
}

// Exited reports an unexpected launcher exit while the scenario is active.
func (node *Node) Exited() <-chan error {
	return node.done
}

// Stop terminates the launcher and waits until Elasticsearch releases its ports.
func (node *Node) Stop() error {
	node.cancelProcess()
	<-node.done

	return waitForPortsAvailable(node.httpPort, node.transportPort)
}

// isReady uses the same authenticated cluster-health signal as the JavaScript prototype.
func (node *Node) isReady(ctx context.Context) (bool, error) {
	request, requestError := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		node.url+"/_cluster/health",
		nil,
	)
	if requestError != nil {
		return false, requestError
	}

	request.SetBasicAuth(elasticsearchUsername, elasticsearchPassword)

	response, responseError := readinessHTTPClient.Do(request)
	if responseError != nil {
		return false, responseError
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("cluster health returned %s", response.Status)
	}

	return true, nil
}

// prepareNodeDirectory isolates installations while sharing Kibana's snapshot cache.
func prepareNodeDirectory(
	kibanaDirectory string,
	nodeDirectory string,
	dataDirectory string,
	logDirectory string,
) error {
	if removeError := os.RemoveAll(dataDirectory); removeError != nil {
		return fmt.Errorf("reset Elasticsearch data: %w", removeError)
	}

	if removeError := os.RemoveAll(logDirectory); removeError != nil {
		return fmt.Errorf("reset Elasticsearch logs: %w", removeError)
	}

	sharedCache := filepath.Join(kibanaDirectory, ".es", "cache")
	if createError := os.MkdirAll(sharedCache, 0o755); createError != nil {
		return fmt.Errorf("create Kibana ES cache: %w", createError)
	}

	if createError := os.MkdirAll(nodeDirectory, 0o755); createError != nil {
		return fmt.Errorf("create Elasticsearch directory: %w", createError)
	}

	nodeCache := filepath.Join(nodeDirectory, "cache")
	if removeError := os.RemoveAll(nodeCache); removeError != nil {
		return fmt.Errorf("reset Elasticsearch cache link: %w", removeError)
	}

	if symlinkError := os.Symlink(sharedCache, nodeCache); symlinkError != nil {
		return fmt.Errorf("share Kibana ES cache: %w", symlinkError)
	}

	return nil
}

// createLogFile resets output from a previous execution.
func createLogFile(logPath string) (*os.File, error) {
	if directoryError := os.MkdirAll(filepath.Dir(logPath), 0o755); directoryError != nil {
		return nil, fmt.Errorf("create Elasticsearch log directory: %w", directoryError)
	}

	logFile, openError := os.Create(logPath)
	if openError != nil {
		return nil, fmt.Errorf("create Elasticsearch log: %w", openError)
	}

	return logFile, nil
}

// recentLog keeps only enough process output to explain a startup failure.
func recentLog(logPath string) string {
	contents, readError := os.ReadFile(logPath)
	if readError != nil {
		return "unable to read log: " + readError.Error()
	}

	if len(contents) > recentLogBytes {
		contents = contents[len(contents)-recentLogBytes:]
	}

	return strings.TrimSpace(string(contents))
}

// latestLogLine shows whether Kibana is downloading, extracting, or starting Elasticsearch.
func latestLogLine(logPath string) string {
	lines := strings.Split(recentLog(logPath), "\n")

	return strings.TrimSpace(lines[len(lines)-1])
}

// assertPortAvailable reports stale local infrastructure before startup.
func assertPortAvailable(port int) error {
	address := net.JoinHostPort(localHost, strconv.Itoa(port))
	listener, listenError := net.Listen("tcp", address)
	if listenError != nil {
		return fmt.Errorf("port %d is unavailable: %w", port, listenError)
	}
	defer listener.Close()

	return nil
}

// waitForPortsAvailable prevents an immediate restart from racing Elasticsearch shutdown.
func waitForPortsAvailable(ports ...int) error {
	timeout := time.NewTimer(shutdownTimeout)
	defer timeout.Stop()

	checkTicker := time.NewTicker(100 * time.Millisecond)
	defer checkTicker.Stop()

	var lastError error

	for {
		allPortsAreAvailable := true

		for _, port := range ports {
			portError := assertPortAvailable(port)
			if portError != nil {
				allPortsAreAvailable = false
				lastError = portError

				break
			}
		}

		if allPortsAreAvailable {
			return nil
		}

		select {
		case <-timeout.C:
			return fmt.Errorf("timed out waiting for Elasticsearch ports to be released: %w", lastError)
		case <-checkTicker.C:
		}
	}
}

// snapshotArguments isolates the two nodes while leaving Kibana's usual security
// setup in place. Disabling security instead would start Elasticsearch without the
// native realm, which makes Kibana return 404s and 500s from every route that asks
// Elasticsearch about privileges. Transport TLS stays off because these are local
// single-node clusters, where the bootstrap checks that would require it do not run.
func snapshotArguments(
	scriptPath string,
	nodeDirectory string,
	dataDirectory string,
	logDirectory string,
	options Options,
) []string {
	return []string{
		scriptPath,
		"snapshot",
		"--base-path",
		nodeDirectory,
		"--use-cached",
		"-E", "cluster.name=degradation-" + options.ID,
		"-E", "node.name=" + options.ID,
		"-E", "network.host=" + localHost,
		"-E", "discovery.type=single-node",
		"-E", "http.port=" + strconv.Itoa(options.HTTPPort),
		"-E", "transport.port=" + strconv.Itoa(options.TransportPort),
		"-E", "path.data=" + dataDirectory,
		"-E", "path.logs=" + logDirectory,
		"-E", "xpack.security.transport.ssl.enabled=false",
		// A fan-out fixture creates one shard per data stream, so the default
		// limit of 1000 shards per node caps the scenario long before the local
		// machine runs out of room.
		"-E", "cluster.max_shards_per_node=" + strconv.Itoa(maxShardsPerNode),
	}
}
