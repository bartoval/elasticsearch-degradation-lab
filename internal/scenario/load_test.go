package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validScenarioYAML = `version: 1
id: local_test
name: Local test
elasticsearch:
  settings: {}
topology:
  remotes: []
fixtures: []
faults: []
`

func writeScenario(t *testing.T, filename string, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), filename)
	if writeError := os.WriteFile(path, []byte(contents), 0o600); writeError != nil {
		t.Fatalf("write scenario: %v", writeError)
	}

	return path
}

func TestLoadAcceptsOneStrictDocument(t *testing.T) {
	loaded, loadError := Load(writeScenario(t, "local__test.yml", validScenarioYAML))
	if loadError != nil {
		t.Fatalf("load valid scenario: %v", loadError)
	}
	if loaded.ID != "local_test" {
		t.Fatalf("loaded id = %q, want local_test", loaded.ID)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	path := writeScenario(t, "local__test.yml", validScenarioYAML+"---\n"+validScenarioYAML)
	_, loadError := Load(path)
	if loadError == nil || !strings.Contains(loadError.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", loadError)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := writeScenario(t, "local__test.yml", validScenarioYAML+"unknown: true\n")
	_, loadError := Load(path)
	if loadError == nil || !strings.Contains(loadError.Error(), "field unknown not found") {
		t.Fatalf("expected unknown-field error, got %v", loadError)
	}
}

func TestLoadRequiresFilenameAndIDToMatch(t *testing.T) {
	path := writeScenario(t, "local__different.yml", validScenarioYAML)
	_, loadError := Load(path)
	if loadError == nil || !strings.Contains(loadError.Error(), "must match filename") {
		t.Fatalf("expected filename/id error, got %v", loadError)
	}
}

func TestLoadRequiresCatalogFilenameShape(t *testing.T) {
	path := writeScenario(t, "local_test.yml", validScenarioYAML)
	_, loadError := Load(path)
	if loadError == nil || !strings.Contains(loadError.Error(), "<topology>__<degradation>") {
		t.Fatalf("expected filename-shape error, got %v", loadError)
	}
}

func TestValidateBulkDocumentsTimeRange(t *testing.T) {
	scenarioConfig := Scenario{
		Version: 1,
		ID:      "local_test",
		Name:    "Local test",
		Fixtures: []Fixture{{
			Target:    CoordinatorTarget,
			Preset:    FixturePresetBulkDocuments,
			Name:      "logs-test",
			Documents: 10,
			TimeRange: "24h",
		}},
	}
	if validationError := scenarioConfig.Validate(); validationError != nil {
		t.Fatalf("validate timeRange: %v", validationError)
	}

	scenarioConfig.Fixtures[0].TimeRange = "yesterday"
	validationError := scenarioConfig.Validate()
	if validationError == nil || !strings.Contains(validationError.Error(), "positive duration") {
		t.Fatalf("expected invalid timeRange error, got %v", validationError)
	}
}

func TestValidateRejectsTimeRangeForOtherPresets(t *testing.T) {
	scenarioConfig := Scenario{
		Version: 1,
		ID:      "local_test",
		Name:    "Local test",
		Fixtures: []Fixture{{
			Target:    CoordinatorTarget,
			Preset:    FixturePresetMinimalLogs,
			Name:      "logs-test",
			TimeRange: "24h",
		}},
	}

	validationError := scenarioConfig.Validate()
	if validationError == nil || !strings.Contains(validationError.Error(), "sizing applies to the bulk presets") {
		t.Fatalf("expected preset-scoping error, got %v", validationError)
	}
}

func TestValidateRejectsRunnerOwnedAndMalformedSettings(t *testing.T) {
	tests := []struct {
		name     string
		setting  string
		expected string
	}{
		{name: "security", setting: "xpack.security.enabled", expected: "runner owns it"},
		{name: "network alias", setting: "http.publish_port", expected: "runner owns it"},
		{name: "malformed", setting: "HTTP PORT", expected: "settings name"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenarioConfig := Scenario{
				Version: 1,
				ID:      "local_test",
				Name:    "Local test",
				Elasticsearch: Elasticsearch{Settings: map[string]string{
					test.setting: "true",
				}},
			}

			validationError := scenarioConfig.Validate()
			if validationError == nil || !strings.Contains(validationError.Error(), test.expected) {
				t.Fatalf("expected %q error, got %v", test.expected, validationError)
			}
		})
	}
}

func TestValidateRejectsUnsafeNames(t *testing.T) {
	scenarioConfig := Scenario{
		Version: 1,
		ID:      "local_test",
		Name:    "Local test",
		Fixtures: []Fixture{{
			Target: CoordinatorTarget,
			Preset: FixturePresetMinimalLogs,
			Name:   "../logs",
		}},
	}

	validationError := scenarioConfig.Validate()
	if validationError == nil || !strings.Contains(validationError.Error(), "lowercase Elasticsearch name") {
		t.Fatalf("expected resource-name error, got %v", validationError)
	}
}
