package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const controlStateName = "runner-state.json"

type scenarioState struct {
	ScenarioID string `json:"scenario"`
	LogPath    string `json:"logPath"`
	Running    bool   `json:"running"`
	Error      string `json:"error,omitempty"`
}

func scenarioControlDirectory() string {
	return filepath.Join("/tmp", "kibana-scenario-runner-"+strconv.Itoa(os.Getuid()))
}

func ensureControlDirectory(directory string) error {
	if createError := os.Mkdir(directory, 0o700); createError != nil && !errors.Is(createError, os.ErrExist) {
		return fmt.Errorf("create scenario control directory: %w", createError)
	}

	info, statError := os.Lstat(directory)
	if statError != nil {
		return fmt.Errorf("inspect scenario control directory: %w", statError)
	}

	stat, hasOwner := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !hasOwner || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("scenario control directory %q must be a private directory owned by the current user", directory)
	}

	return nil
}

func writeScenarioState(directory string, state scenarioState) error {
	stateFile, createError := os.CreateTemp(directory, ".runner-state-*")
	if createError != nil {
		return fmt.Errorf("create scenario state: %w", createError)
	}
	defer os.Remove(stateFile.Name())

	encodeError := json.NewEncoder(stateFile).Encode(state)
	if writeError := errors.Join(encodeError, stateFile.Close()); writeError != nil {
		return fmt.Errorf("write scenario state: %w", writeError)
	}

	if renameError := os.Rename(stateFile.Name(), filepath.Join(directory, controlStateName)); renameError != nil {
		return fmt.Errorf("replace scenario state: %w", renameError)
	}

	return nil
}

func readScenarioState(directory string) (scenarioState, error) {
	contents, readError := os.ReadFile(filepath.Join(directory, controlStateName))
	if readError != nil {
		return scenarioState{}, readError
	}

	var state scenarioState
	if decodeError := json.Unmarshal(contents, &state); decodeError != nil {
		return scenarioState{}, fmt.Errorf("read scenario state: %w", decodeError)
	}

	return state, nil
}

func inactiveScenarioError(directory string, expectedID string) error {
	state, stateError := readScenarioState(directory)
	if errors.Is(stateError, os.ErrNotExist) {
		return nil
	}

	if stateError != nil {
		return stateError
	}

	if expectedID != "" && expectedID != state.ScenarioID {
		return nil
	}

	if state.Error != "" {
		return fmt.Errorf(
			"scenario %q exited with an error: %s\nLogs: %s",
			state.ScenarioID,
			state.Error,
			state.LogPath,
		)
	}

	if state.Running {
		return fmt.Errorf(
			"scenario %q is not reachable; startup or shutdown may still be in progress, or the runner exited without completing cleanup\nLogs: %s",
			state.ScenarioID,
			state.LogPath,
		)
	}

	return nil
}
