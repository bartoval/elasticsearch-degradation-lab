package scenario

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var scenarioFilePattern = regexp.MustCompile(
	`^[a-z0-9]+(?:_[a-z0-9]+)*__[a-z0-9]+(?:_[a-z0-9]+)*(?:__[a-z0-9]+(?:_[a-z0-9]+)*)?\.yml$`,
)

// Load converts a YAML file into a Scenario and validates it before returning it to the CLI.
func Load(scenarioPath string) (Scenario, error) {
	scenarioFile, openError := os.Open(scenarioPath)
	if openError != nil {
		return Scenario{}, fmt.Errorf("open %q: %w", scenarioPath, openError)
	}
	defer scenarioFile.Close()

	decoder := yaml.NewDecoder(scenarioFile)
	decoder.KnownFields(true)

	var loadedScenario Scenario

	decodeError := decoder.Decode(&loadedScenario)
	if decodeError != nil {
		return Scenario{}, fmt.Errorf("read %q: %w", scenarioPath, decodeError)
	}

	var trailingDocument any
	trailingError := decoder.Decode(&trailingDocument)
	if trailingError == nil {
		return Scenario{}, fmt.Errorf("read %q: multiple YAML documents are not allowed", scenarioPath)
	}
	if trailingError != io.EOF {
		return Scenario{}, fmt.Errorf("read trailing content from %q: %w", scenarioPath, trailingError)
	}

	if filenameError := validateFilename(scenarioPath, loadedScenario.ID); filenameError != nil {
		return Scenario{}, filenameError
	}

	validationError := loadedScenario.Validate()
	if validationError != nil {
		return Scenario{}, fmt.Errorf("%q: %w", scenarioPath, validationError)
	}

	return loadedScenario, nil
}

func validateFilename(scenarioPath string, scenarioID string) error {
	filename := filepath.Base(scenarioPath)
	if !scenarioFilePattern.MatchString(filename) {
		return fmt.Errorf(
			"scenario filename %q must follow <topology>__<degradation>[__<fixture>].yml",
			filename,
		)
	}

	stem := strings.TrimSuffix(filename, ".yml")
	expectedID := strings.ReplaceAll(stem, "__", "_")
	if scenarioID != expectedID {
		return fmt.Errorf(
			"scenario id %q must match filename %q as %q",
			scenarioID,
			filename,
			expectedID,
		)
	}

	return nil
}
