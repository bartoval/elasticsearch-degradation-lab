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
	cancel context.CancelFunc
	done   chan error
}

// Start launches the program and returns once it has been spawned, without waiting
// for it to become useful: readiness is the caller's own question to ask.
func Start(ctx context.Context, spec Spec) (*Child, error) {
	processContext, cancelProcess := context.WithCancel(ctx)
	command := exec.CommandContext(processContext, spec.Path, spec.Arguments...)
	command.Dir = spec.Directory
	command.Env = spec.Environment
	command.Stdout = spec.Output
	command.Stderr = spec.Output
	command.WaitDelay = spec.ShutdownGrace

	if spec.ProcessGroup {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	command.Cancel = func() error {
		return terminate(command.Process, spec.ProcessGroup)
	}

	if startError := command.Start(); startError != nil {
		cancelProcess()

		return nil, startError
	}

	child := &Child{
		cancel: cancelProcess,
		done:   make(chan error, 1),
	}

	go func() {
		child.done <- command.Wait()
		close(child.done)
	}()

	return child, nil
}

// Exited reports the program's exit. Reading from it is how a caller learns that a
// process died on its own rather than because it was asked to.
func (child *Child) Exited() <-chan error {
	return child.done
}

// Stop asks the program to exit and waits for it. It is safe to call more than
// once, so a caller may stop explicitly without unbalancing a deferred stop.
func (child *Child) Stop() error {
	child.cancel()

	return <-child.done
}

// terminate asks a process, or its whole group, to exit. A process that is already
// gone is the outcome that was wanted, not a failure.
func terminate(started *os.Process, processGroup bool) error {
	if !processGroup {
		signalError := started.Signal(syscall.SIGTERM)
		if errors.Is(signalError, os.ErrProcessDone) {
			return nil
		}

		return signalError
	}

	signalError := syscall.Kill(-started.Pid, syscall.SIGTERM)
	if errors.Is(signalError, syscall.ESRCH) {
		return nil
	}
	if signalError != nil {
		return fmt.Errorf("signal process group: %w", signalError)
	}

	return nil
}
