package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	controlSocketName = "runner.sock"
	controlLockName   = "runner.lock"
	controlTimeout    = 5 * time.Second
	stopTimeout       = 2 * time.Minute
)

var errNoActiveScenario = errors.New("no scenario is running")

type stopRequest struct {
	ScenarioID string `json:"scenario"`
}

type stopResponse struct {
	ScenarioID string `json:"scenario"`
	Error      string `json:"error,omitempty"`
}

type scenarioControl struct {
	directory  string
	state      scenarioState
	listener   *net.UnixListener
	lock       *os.File
	cancel     context.CancelFunc
	finished   chan struct{}
	stopError  error
	handlers   sync.WaitGroup
	closeOnce  sync.Once
	closeError error
}

func acquireScenarioLock(controlDirectory string) (*os.File, error) {
	if directoryError := ensureControlDirectory(controlDirectory); directoryError != nil {
		return nil, directoryError
	}

	lock, openError := os.OpenFile(filepath.Join(controlDirectory, controlLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if openError != nil {
		return nil, fmt.Errorf("open scenario lock: %w", openError)
	}

	if lockError := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockError != nil {
		_ = lock.Close()

		if errors.Is(lockError, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another scenario is already running in %q; stop it first", controlDirectory)
		}

		return nil, fmt.Errorf("lock scenario runtime: %w", lockError)
	}

	return lock, nil
}

func openScenarioControl(
	controlDirectory string,
	state scenarioState,
	lock *os.File,
	cancel context.CancelFunc,
) (*scenarioControl, error) {
	socketPath := filepath.Join(controlDirectory, controlSocketName)
	if removeError := os.Remove(socketPath); removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale scenario socket: %w", removeError)
	}

	listener, listenError := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if listenError != nil {
		return nil, fmt.Errorf("listen for scenario stop requests: %w", listenError)
	}

	if permissionError := os.Chmod(socketPath, 0o600); permissionError != nil {
		_ = listener.Close()

		return nil, fmt.Errorf("restrict scenario socket access: %w", permissionError)
	}

	state.Running = true
	if stateError := writeScenarioState(controlDirectory, state); stateError != nil {
		_ = listener.Close()

		return nil, stateError
	}

	control := &scenarioControl{
		directory: controlDirectory,
		state:     state,
		listener:  listener,
		lock:      lock,
		cancel:    cancel,
		finished:  make(chan struct{}),
	}
	control.handlers.Add(1)
	go control.serve()

	return control, nil
}

func requestScenarioStop(ctx context.Context, controlDirectory string, scenarioID string) (string, error) {
	var dialer net.Dialer
	connection, connectError := dialer.DialContext(ctx, "unix", filepath.Join(controlDirectory, controlSocketName))
	if connectError != nil {
		if errors.Is(connectError, os.ErrNotExist) || errors.Is(connectError, syscall.ECONNREFUSED) {
			return "", errNoActiveScenario
		}

		return "", fmt.Errorf("connect to scenario runner: %w", connectError)
	}
	defer connection.Close()

	stopClosing := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopClosing()

	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if deadlineError := connection.SetDeadline(deadline); deadlineError != nil {
			return "", deadlineError
		}
	}

	if sendError := json.NewEncoder(connection).Encode(stopRequest{ScenarioID: scenarioID}); sendError != nil {
		return "", fmt.Errorf("send scenario stop request: %w", sendError)
	}

	var response stopResponse
	if receiveError := json.NewDecoder(connection).Decode(&response); receiveError != nil {
		if ctx.Err() != nil {
			return "", &stopWaitError{Err: ctx.Err()}
		}

		if errors.Is(receiveError, os.ErrDeadlineExceeded) {
			return "", &stopWaitError{Err: context.DeadlineExceeded}
		}

		return "", fmt.Errorf("wait for scenario cleanup: %w", receiveError)
	}

	if response.Error != "" {
		return "", errors.New(response.Error)
	}

	return response.ScenarioID, nil
}

func (control *scenarioControl) close(stopError error) error {
	control.closeOnce.Do(func() {
		listenerError := control.listener.Close()
		state := control.state
		state.Running = false
		if stopError != nil {
			state.Error = stopError.Error()
		}

		stateError := writeScenarioState(control.directory, state)
		control.closeError = errors.Join(listenerError, stateError, control.lock.Close())
		control.stopError = errors.Join(stopError, control.closeError)
		close(control.finished)
		control.handlers.Wait()
	})

	return control.closeError
}

func (control *scenarioControl) serve() {
	defer control.handlers.Done()

	for {
		connection, acceptError := control.listener.AcceptUnix()
		if acceptError != nil {
			return
		}

		control.handlers.Add(1)
		go control.handleStop(connection)
	}
}

func (control *scenarioControl) handleStop(connection *net.UnixConn) {
	defer control.handlers.Done()
	defer connection.Close()

	if deadlineError := connection.SetReadDeadline(time.Now().Add(controlTimeout)); deadlineError != nil {
		return
	}

	var request stopRequest
	if decodeError := json.NewDecoder(connection).Decode(&request); decodeError != nil {
		return
	}

	response := stopResponse{ScenarioID: control.state.ScenarioID}
	if request.ScenarioID != "" && request.ScenarioID != control.state.ScenarioID {
		response.Error = fmt.Sprintf(
			"scenario %q is running, not %q",
			control.state.ScenarioID,
			request.ScenarioID,
		)
	} else {
		control.cancel()
		<-control.finished

		if control.stopError != nil {
			response.Error = control.stopError.Error()
		}
	}

	if deadlineError := connection.SetWriteDeadline(time.Now().Add(controlTimeout)); deadlineError != nil {
		return
	}

	_ = json.NewEncoder(connection).Encode(response)
}
