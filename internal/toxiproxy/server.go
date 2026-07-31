package toxiproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
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
	cancelProcess context.CancelFunc
	done          chan error
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

	processContext, cancelProcess := context.WithCancel(ctx)
	command := exec.CommandContext(
		processContext,
		binaryPath,
		"-host="+localHost,
		"-port="+apiPort,
	)
	command.Stdout = logFile
	command.Stderr = logFile
	command.WaitDelay = shutdownTimeout
	command.Cancel = func() error {
		signalError := command.Process.Signal(syscall.SIGTERM)
		if errors.Is(signalError, os.ErrProcessDone) {
			return nil
		}

		return signalError
	}

	startError := command.Start()
	if startError != nil {
		cancelProcess()

		return nil, fmt.Errorf("start Toxiproxy: %w", startError)
	}

	server := &Server{
		cancelProcess: cancelProcess,
		done:          make(chan error, 1),
	}

	go func() {
		server.done <- command.Wait()
		close(server.done)
	}()

	return server, nil
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
		case waitError := <-server.done:
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

// Exited reports an unexpected Toxiproxy exit while the scenario is active.
func (server *Server) Exited() <-chan error {
	return server.done
}

// Stop cancels the child context and waits for the process to release its resources.
func (server *Server) Stop() {
	server.cancelProcess()
	<-server.done
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
