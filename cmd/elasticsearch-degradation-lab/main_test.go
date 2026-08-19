package main

import "testing"

func TestLoadScenarioResolvesEmbeddedCatalogName(t *testing.T) {
	loadedScenario, loadError := loadScenario("local__rows500k")
	if loadError != nil {
		t.Fatalf("load embedded scenario: %v", loadError)
	}
	if loadedScenario.ID != "local_rows500k" {
		t.Fatalf("scenario id = %q, want local_rows500k", loadedScenario.ID)
	}
}
