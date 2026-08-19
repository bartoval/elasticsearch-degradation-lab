package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// managedComponent is the lifecycle contract shared by every process the runner
// owns. Keeping it private avoids turning implementation details into a public
// abstraction while giving monitoring and teardown one source of truth.
type managedComponent struct {
	name    string
	logPath string
	done    <-chan struct{}
	wait    func() error
	stop    func() error
}

// Environment is a running scenario: every process started, connected and degraded
// as the scenario asked.
type Environment struct {
	components       []managedComponent
	sessionDirectory string

	buildCancel  context.CancelCauseFunc
	failureDone  chan struct{}
	failureOnce  sync.Once
	failureError error
	stopping     chan struct{}
	stopOnce     sync.Once
	stopError    error
}

// UnexpectedExitError reports a process that ended while the scenario was still
// meant to be running. It is a distinct type because it means something different
// from a startup failure: the environment was correct and then stopped being so.
type UnexpectedExitError struct {
	Component string
	LogPath   string
	Err       error
}

func (exitError *UnexpectedExitError) Error() string {
	if exitError.Err == nil {
		return fmt.Sprintf("%s exited while the scenario was active", exitError.Component)
	}

	return fmt.Sprintf(
		"%s exited while the scenario was active: %v",
		exitError.Component,
		exitError.Err,
	)
}

func (exitError *UnexpectedExitError) Unwrap() error {
	return exitError.Err
}

// registerComponent records ownership in startup order and immediately supervises
// the component. Teardown can then use the reverse order without duplicating a
// field and a conditional for every process type.
func (environment *Environment) registerComponent(component managedComponent) {
	environment.components = append(environment.components, component)
	environment.monitorComponent(component)
}

// Wait holds the environment open until the caller cancels or a process dies.
func (environment *Environment) Wait(ctx context.Context) error {
	select {
	case <-environment.failureDone:
		return environment.failureError
	default:
	}

	select {
	case <-ctx.Done():
		select {
		case <-environment.failureDone:
			return environment.failureError
		default:
			return nil
		}
	case <-environment.failureDone:
		return environment.failureError
	}
}

// monitorComponent turns the first child death into one cached environment failure.
// It starts as soon as each child is registered, so build steps are supervised too.
func (environment *Environment) monitorComponent(component managedComponent) {
	go func() {
		select {
		case <-environment.stopping:
			return
		case <-component.done:
		}

		select {
		case <-environment.stopping:
			return
		default:
		}

		exitError := &UnexpectedExitError{
			Component: component.name,
			LogPath:   component.logPath,
			Err:       component.wait(),
		}
		environment.failureOnce.Do(func() {
			environment.failureError = exitError
			close(environment.failureDone)
			environment.buildCancel(exitError)
		})
	}()
}

// Stop tears components down in reverse startup order. This naturally keeps
// Elasticsearch ahead of Toxiproxy and remains correct when a new component is
// inserted into the startup sequence.
func (environment *Environment) Stop() error {
	environment.stopOnce.Do(func() {
		close(environment.stopping)
		environment.buildCancel(context.Canceled)

		var stopErrors []error
		for componentIndex := len(environment.components) - 1; componentIndex >= 0; componentIndex-- {
			component := environment.components[componentIndex]
			if stopError := component.stop(); stopError != nil {
				stopErrors = append(stopErrors, fmt.Errorf("stop %s: %w", component.name, stopError))
			}
		}

		if len(stopErrors) == 0 {
			if cleanupError := cleanupSessionElasticsearch(environment.sessionDirectory); cleanupError != nil {
				stopErrors = append(stopErrors, cleanupError)
			}
		}

		environment.stopError = errors.Join(stopErrors...)
	})

	return environment.stopError
}

// cleanupSessionElasticsearch removes only the large per-run installation, data,
// and internal logs. Operator-facing logs live beside it and remain available.
func cleanupSessionElasticsearch(sessionDirectory string) error {
	elasticsearchDirectory := filepath.Join(sessionDirectory, "elasticsearch")
	if removeError := os.RemoveAll(elasticsearchDirectory); removeError != nil {
		return fmt.Errorf(
			"remove Elasticsearch runtime %q: %w",
			elasticsearchDirectory,
			removeError,
		)
	}

	return nil
}
