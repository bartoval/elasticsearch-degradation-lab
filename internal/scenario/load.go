package scenario

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
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

	validationError := loadedScenario.Validate()
	if validationError != nil {
		return Scenario{}, fmt.Errorf("%q: %w", scenarioPath, validationError)
	}

	return loadedScenario, nil
}
