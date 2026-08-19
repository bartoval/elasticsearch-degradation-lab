package toxiproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/process"
)

const (
	localHost         = "127.0.0.1"
	apiPort           = "8474"
	apiAddress        = localHost + ":" + apiPort
	readinessTimeout  = 10 * time.Second
	readinessInterval = 200 * time.Millisecond
	requestTimeout    = 1 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// APIURL is the local HTTP endpoint used to configure the running Toxiproxy server.
const APIURL = "http://" + apiAddress

// Server owns the Toxiproxy child process and reports when that process exits.
type Server struct {
	child *process.Child
}

// Start launches Toxiproxy with its API bound only to the local machine.
func Start(ctx context.Context, binaryPath string, logPath string) (*Server, error) {
	addressError := assertAPIAddressAvailable()
	if addressError != nil {
		return nil, addressError
	}

	createDirectoryError := os.MkdirAll(filepath.Dir(logPath), 0o755)
	if createDirectoryError != nil {
		return nil, fmt.Errorf("create Toxiproxy log directory: %w", createDirectoryError)
	}

	logFile, openError := os.Create(logPath)
	if openError != nil {
		return nil, fmt.Errorf("create Toxiproxy log: %w", openError)
	}
	defer logFile.Close()

	child, startError := process.Start(ctx, process.Spec{
		Path: binaryPath,
		Arguments: []string{
			"-host=" + localHost,
			"-port=" + apiPort,
			// Per-proxy byte counters, which are what turns "the request looks
			// faster" into "no bytes crossed", and what a bandwidth fault has to be
			// calibrated against, since the bytes it charges for are the ones on the
			// transport rather than the ones in the answer Elasticsearch assembles.
			"-proxy-metrics",
		},
		Output:        logFile,
		ShutdownGrace: shutdownTimeout,
	})
	if startError != nil {
		return nil, fmt.Errorf("start Toxiproxy: %w", startError)
	}

	return &Server{child: child}, nil
}

// WaitUntilReady confirms that the child process accepts HTTP API requests.
func (server *Server) WaitUntilReady(ctx context.Context) error {
	readinessContext, cancelReadiness := context.WithTimeout(ctx, readinessTimeout)
	defer cancelReadiness()

	readinessTicker := time.NewTicker(readinessInterval)
	defer readinessTicker.Stop()

	for {
		if apiIsAvailable(readinessContext) {
			return nil
		}

		select {
		case <-server.child.Done():
			waitError := server.child.Wait()
			if waitError != nil {
				return fmt.Errorf("Toxiproxy exited before becoming ready: %w", waitError)
			}

			return fmt.Errorf("Toxiproxy exited before becoming ready")
		case <-readinessContext.Done():
			return fmt.Errorf("wait for Toxiproxy API: %w", readinessContext.Err())
		case <-readinessTicker.C:
		}
	}
}

// Done is closed when Toxiproxy exits.
func (server *Server) Done() <-chan struct{} {
	return server.child.Done()
}

// Wait returns Toxiproxy's cached exit outcome.
func (server *Server) Wait() error {
	return server.child.Wait()
}

// Stop asks Toxiproxy to exit and waits for it to release its port.
func (server *Server) Stop() error {
	return server.child.Stop()
}

// assertAPIAddressAvailable reports a stale Toxiproxy process before starting another one.
func assertAPIAddressAvailable() error {
	listener, listenError := net.Listen("tcp", apiAddress)
	if listenError != nil {
		return fmt.Errorf("Toxiproxy API address %s is already in use", apiAddress)
	}

	closeError := listener.Close()
	if closeError != nil {
		return fmt.Errorf("release Toxiproxy API address %s: %w", apiAddress, closeError)
	}

	return nil
}

// apiIsAvailable uses the public version endpoint as the readiness signal.
func apiIsAvailable(ctx context.Context) bool {
	requestContext, cancelRequest := context.WithTimeout(ctx, requestTimeout)
	defer cancelRequest()

	request, requestError := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		APIURL+"/version",
		nil,
	)
	if requestError != nil {
		return false
	}

	response, responseError := http.DefaultClient.Do(request)
	if responseError != nil {
		return false
	}
	defer response.Body.Close()

	_, _ = io.Copy(io.Discard, response.Body)

	return response.StatusCode == http.StatusOK
}
