package scenario

import (
	"strings"
	"testing"
)

func TestLoadArchiveAndMultiplicationFixtures(testContext *testing.T) {
	contents := strings.Replace(validScenarioYAML, "fixtures: []", `fixtures:
  - target: coordinator
    preset: es_archive
    name: seed
    archive: fixtures/example
  - target: coordinator
    preset: multiply_documents
    name: logs-expanded
    source: logs-seed
    copies: 10`, 1)

	loaded, loadError := LoadBytes("local__test.yml", []byte(contents))
	if loadError != nil {
		testContext.Fatalf("load composed fixtures: %v", loadError)
	}

	if len(loaded.Fixtures) != 2 {
		testContext.Fatalf("fixture count = %d, want 2", len(loaded.Fixtures))
	}

	if loaded.Fixtures[0].Archive != "fixtures/example" || loaded.Fixtures[1].Copies != 10 ||
		loaded.Fixtures[1].Source != "logs-seed" {
		testContext.Fatalf("unexpected fixtures: %+v", loaded.Fixtures)
	}
}

func TestValidateRejectsDuplicateArchiveLogNamesAcrossTargets(testContext *testing.T) {
	scenarioConfig := Scenario{
		Version:  1,
		ID:       "local_test",
		Name:     "Local test",
		Topology: Topology{Remotes: []Remote{{ID: "remote-1", Alias: "remote"}}},
		Fixtures: []Fixture{
			{Target: CoordinatorTarget, Preset: FixturePresetESArchive, Name: "seed", Archive: "fixture-one"},
			{Target: "remote-1", Preset: FixturePresetESArchive, Name: "seed", Archive: "fixture-two"},
		},
	}

	validationError := scenarioConfig.Validate()
	if validationError == nil || !strings.Contains(validationError.Error(), "duplicated for archive logs") {
		testContext.Fatalf("expected duplicate archive name error, got %v", validationError)
	}
}

func TestValidateArchiveAndMultiplicationKnobs(testContext *testing.T) {
	testCases := []struct {
		name     string
		fixture  Fixture
		expected string
	}{
		{name: "missing archive", fixture: Fixture{Preset: FixturePresetESArchive}, expected: "archive is required"},
		{name: "archive bulk sizing", fixture: Fixture{Preset: FixturePresetESArchive, Archive: "fixture", Documents: 1}, expected: "sizing applies"},
		{name: "archive on bulk preset", fixture: Fixture{Preset: FixturePresetBulkDocuments, Archive: "fixture", Documents: 1}, expected: "archive applies"},
		{name: "copy options on archive", fixture: Fixture{Preset: FixturePresetESArchive, Archive: "fixture", Copies: 2}, expected: "source and copies apply"},
		{name: "missing source", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Copies: 2}, expected: "source must be one"},
		{name: "source wildcard", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Source: "logs-*", Copies: 2}, expected: "source must be one"},
		{name: "same source and destination", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Source: "logs-expanded", Copies: 2}, expected: "source must differ"},
		{name: "missing copies", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Source: "logs-seed"}, expected: "copies must be between"},
		{name: "too many copies", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Source: "logs-seed", Copies: MaxDocumentCopies + 1}, expected: "copies must be between"},
		{name: "copy bulk sizing", fixture: Fixture{Preset: FixturePresetMultiplyDocuments, Source: "logs-seed", Copies: 2, TimeRange: "24h"}, expected: "sizing applies"},
	}

	for _, testCase := range testCases {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			fixture := testCase.fixture
			fixture.Target = CoordinatorTarget
			fixture.Name = "logs-expanded"
			scenarioConfig := Scenario{Version: 1, ID: "local_test", Name: "Local test", Fixtures: []Fixture{fixture}}

			validationError := scenarioConfig.Validate()
			if validationError == nil || !strings.Contains(validationError.Error(), testCase.expected) {
				testContext.Fatalf("expected %q error, got %v", testCase.expected, validationError)
			}
		})
	}
}
