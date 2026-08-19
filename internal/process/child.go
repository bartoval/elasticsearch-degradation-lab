// Package process supervises the external programs a scenario depends on.
//
// The runner is only useful while every process it started is still running, so
// the same three guarantees are needed for each of them: a stop that actually
// reaches the whole process tree, a bounded wait before giving up on a graceful
// exit, and a way for the caller to notice a death it did not ask for. Keeping
// them in one place is what stops the two supervisors from drifting apart, which
// matters most for the parts that are easy to get subtly wrong.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Spec describes a program to run under supervision.
type Spec struct {
	Path      string
	Arguments []string
	Directory string
	// Environment replaces the inherited environment when set.
	Environment []string
	// Output receives both stdout and stderr.
	Output io.Writer
	// ProcessGroup signals the whole group rather than the direct child, which is
	// required when that child is a launcher whose real work happens in processes
	// it spawns: signalling only the launcher would orphan them.
	ProcessGroup bool
	// ShutdownGrace bounds how long a graceful stop may take before the process is
	// killed outright.
	ShutdownGrace time.Duration
}

// Child is a started program whose exit the caller can observe and cause.
type Child struct {
	process       *os.Process
	processGroup  bool
	shutdownGrace time.Duration
	done          chan struct{}
	waitError     error
	stopOnce      sync.Once
	stopError     error
}

// Start launches the program and returns once it has been spawned, without waiting
// for it to become useful: readiness is the caller's own question to ask.
func Start(ctx context.Context, spec Spec) (*Child, error) {
	if contextError := ctx.Err(); contextError != nil {
		return nil, contextError
	}

	command := exec.Command(spec.Path, spec.Arguments...)
	command.Dir = spec.Directory
	command.Env = spec.Environment
	command.Stdout = spec.Output
	command.Stderr = spec.Output

	if spec.ProcessGroup {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	if startError := command.Start(); startError != nil {
		return nil, startError
	}

	child := &Child{
		process:       command.Process,
		processGroup:  spec.ProcessGroup,
		shutdownGrace: spec.ShutdownGrace,
		done:          make(chan struct{}),
	}

	go func() {
		child.waitError = command.Wait()
		close(child.done)
	}()

	// A caller may still choose context ownership for a standalone child. The
	// runner itself uses an independent context and performs its ordered teardown
	// explicitly, so cancellation cannot stop all of its processes at once.
	go func() {
		select {
		case <-ctx.Done():
			_ = child.Stop()
		case <-child.done:
		}
	}()

	return child, nil
}

// Done is closed when the program exits. Unlike a channel carrying one error, a
// closed signal broadcasts the outcome to every observer without consuming it.
func (child *Child) Done() <-chan struct{} {
	return child.done
}

// Wait returns the cached process outcome to any number of callers.
func (child *Child) Wait() error {
	<-child.done

	return child.waitError
}

// Stop asks the program to exit and waits for it. A process exit status caused by
// the requested signal is expected; only failures to control the process are
// returned. Repeated and concurrent calls receive the same result.
func (child *Child) Stop() error {
	child.stopOnce.Do(func() {
		child.stopError = child.stop()
	})

	return child.stopError
}

func (child *Child) stop() error {
	if child.targetExited() {
		<-child.done

		return nil
	}

	terminateError := sendSignal(child.process, child.processGroup, syscall.SIGTERM)
	if terminateError != nil {
		terminateError = fmt.Errorf("terminate process: %w", terminateError)
	}

	if child.waitForTargetExit(child.shutdownGrace) {
		return terminateError
	}

	killError := sendSignal(child.process, child.processGroup, syscall.SIGKILL)
	if killError != nil {
		killError = fmt.Errorf("kill process after %s grace: %w", child.shutdownGrace, killError)
	}

	if !child.waitForTargetExit(child.shutdownGrace) {
		killError = errors.Join(
			killError,
			fmt.Errorf("process target still exists after SIGKILL"),
		)
	}

	return errors.Join(terminateError, killError)
}

func (child *Child) targetExited() bool {
	if !child.processGroup {
		select {
		case <-child.done:
			return true
		default:
			return false
		}
	}

	groupError := syscall.Kill(-child.process.Pid, 0)

	return errors.Is(groupError, syscall.ESRCH)
}

func (child *Child) waitForTargetExit(timeout time.Duration) bool {
	if child.targetExited() {
		<-child.done

		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			return false
		case <-ticker.C:
			if child.targetExited() {
				<-child.done

				return true
			}
		}
	}
}

// signal reaches either one process or its whole group. A process that is already
// gone is the outcome Stop wanted, not a failure.
func sendSignal(started *os.Process, processGroup bool, requested os.Signal) error {
	if !processGroup {
		signalError := started.Signal(requested)
		if errors.Is(signalError, os.ErrProcessDone) {
			return nil
		}

		return signalError
	}

	unixSignal, validSignal := requested.(syscall.Signal)
	if !validSignal {
		return fmt.Errorf("unsupported process-group signal %T", requested)
	}

	signalError := syscall.Kill(-started.Pid, unixSignal)
	if errors.Is(signalError, syscall.ESRCH) {
		return nil
	}
	if signalError != nil {
		return fmt.Errorf("signal process group: %w", signalError)
	}

	return nil
}
