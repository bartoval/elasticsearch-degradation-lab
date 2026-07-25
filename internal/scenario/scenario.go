package scenario

import "fmt"

const (
	supportedVersion    = 1
	coordinatorTarget   = "coordinator"
	latencyFaultType    = "latency"
	minimalLogsPreset   = "minimal_logs"
	minimalLookupPreset = "minimal_lookup"
)

// Scenario describes the infrastructure, test data, and network faults requested by a YAML file.
type Scenario struct {
	Version  int       `yaml:"version"`
	ID       string    `yaml:"id"`
	Name     string    `yaml:"name"`
	Topology Topology  `yaml:"topology"`
	Fixtures []Fixture `yaml:"fixtures"`
	Faults   []Fault   `yaml:"faults"`
}

// Topology identifies the remote Elasticsearch clusters required by the scenario.
type Topology struct {
	Remotes []Remote `yaml:"remotes"`
}

// Remote describes one Elasticsearch cluster connected to the coordinator.
type Remote struct {
	ID              string `yaml:"id"`
	Alias           string `yaml:"alias"`
	SkipUnavailable bool   `yaml:"skipUnavailable"`
}

// Fixture selects the data preset that will be created on a scenario target.
type Fixture struct {
	Target string `yaml:"target"`
	Preset string `yaml:"preset"`
}

// Fault describes a network degradation applied to a scenario target.
type Fault struct {
	ID        string `yaml:"id"`
	Target    string `yaml:"target"`
	Type      string `yaml:"type"`
	LatencyMS int    `yaml:"latencyMs"`
	JitterMS  int    `yaml:"jitterMs"`
}

// Validate rejects scenarios that cannot be executed predictably by the current runner.
func (scenarioConfig Scenario) Validate() error {
	if scenarioConfig.Version != supportedVersion {
		return fmt.Errorf("version must be %d", supportedVersion)
	}

	if scenarioConfig.ID == "" {
		return fmt.Errorf("id is required")
	}

	if scenarioConfig.Name == "" {
		return fmt.Errorf("name is required")
	}

	if len(scenarioConfig.Topology.Remotes) == 0 {
		return fmt.Errorf("topology.remotes must contain at least one remote")
	}

	targets := map[string]bool{
		coordinatorTarget: true,
	}

	for remoteIndex, remote := range scenarioConfig.Topology.Remotes {
		if remote.ID == "" {
			return fmt.Errorf("topology.remotes[%d].id is required", remoteIndex)
		}

		if remote.Alias == "" {
			return fmt.Errorf("topology.remotes[%d].alias is required", remoteIndex)
		}

		if targets[remote.ID] {
			return fmt.Errorf("topology.remotes[%d].id %q is duplicated", remoteIndex, remote.ID)
		}

		targets[remote.ID] = true
	}

	for fixtureIndex, fixture := range scenarioConfig.Fixtures {
		if !targets[fixture.Target] {
			return fmt.Errorf(
				"fixtures[%d].target %q does not match a configured target",
				fixtureIndex,
				fixture.Target,
			)
		}

		if fixture.Preset == "" {
			return fmt.Errorf("fixtures[%d].preset is required", fixtureIndex)
		}

		presetIsSupported := fixture.Preset == minimalLogsPreset ||
			fixture.Preset == minimalLookupPreset
		if !presetIsSupported {
			return fmt.Errorf(
				"fixtures[%d].preset %q is not supported",
				fixtureIndex,
				fixture.Preset,
			)
		}
	}

	if len(scenarioConfig.Faults) == 0 {
		return fmt.Errorf("faults must contain at least one fault")
	}

	for faultIndex, fault := range scenarioConfig.Faults {
		if fault.ID == "" {
			return fmt.Errorf("faults[%d].id is required", faultIndex)
		}

		if !targets[fault.Target] {
			return fmt.Errorf(
				"faults[%d].target %q does not match a configured target",
				faultIndex,
				fault.Target,
			)
		}

		if fault.Type != latencyFaultType {
			return fmt.Errorf(
				"faults[%d].type must be %q",
				faultIndex,
				latencyFaultType,
			)
		}

		if fault.LatencyMS <= 0 {
			return fmt.Errorf("faults[%d].latencyMs must be greater than zero", faultIndex)
		}

		if fault.JitterMS < 0 {
			return fmt.Errorf("faults[%d].jitterMs cannot be negative", faultIndex)
		}
	}

	return nil
}
