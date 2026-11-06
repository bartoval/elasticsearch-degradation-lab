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

func TestPlayAndStopRejectInvalidArguments(testContext *testing.T) {
	testCases := []struct {
		name      string
		arguments []string
	}{
		{name: "missing play scenario", arguments: []string{"play"}},
		{name: "unknown play option", arguments: []string{"play", "local__rows500k", "--unknown"}},
		{name: "missing checkout path", arguments: []string{"play", "local__rows500k", "--kibana-dir"}},
		{name: "extra stop arguments", arguments: []string{"stop", "local__rows500k", "extra"}},
	}

	for _, testCase := range testCases {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			if exitCode := run(testCase.arguments); exitCode != exitInvalidArguments {
				testContext.Fatalf("exit code = %d, want %d", exitCode, exitInvalidArguments)
			}
		})
	}
}
