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

func TestLoadArchiveScenarioComposesCCSMultiplicationAndFaults(testContext *testing.T) {
	loadedScenario, loadError := loadScenario("fanout20__tail3s__archive10")
	if loadError != nil {
		testContext.Fatalf("load archive scenario: %v", loadError)
	}

	if len(loadedScenario.Topology.Remotes) != 1 || len(loadedScenario.Fixtures) != 2 || len(loadedScenario.Faults) != 1 {
		testContext.Fatalf("unexpected composed scenario shape: %+v", loadedScenario)
	}

	if loadedScenario.Topology.Remotes[0].AliasCount != 20 ||
		loadedScenario.Fixtures[0].Archive == "" ||
		loadedScenario.Fixtures[1].Source != "logstash-2015.09.22" ||
		loadedScenario.Fixtures[1].Copies != 10 ||
		loadedScenario.Faults[0].AliasPosition != 20 {
		testContext.Fatalf("unexpected composed scenario: %+v", loadedScenario)
	}
}
