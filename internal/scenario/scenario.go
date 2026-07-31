package scenario

import (
	"fmt"
	"path/filepath"
	"strconv"
)

const (
	supportedVersion  = 1
	latencyFaultType  = "latency"
	CoordinatorTarget = "coordinator"
)

// FixturePreset limits YAML values to data setups implemented by the runner.
type FixturePreset string

const (
	FixturePresetMinimalLogs   FixturePreset = "minimal_logs"
	FixturePresetMinimalLookup FixturePreset = "minimal_lookup"
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
	AliasCount      int    `yaml:"aliasCount"`
	SkipUnavailable bool   `yaml:"skipUnavailable"`
}

// Aliases returns one explicit alias or a numbered set for fan-out scenarios.
func (remote Remote) Aliases() []string {
	if remote.AliasCount <= 1 {
		return []string{remote.Alias}
	}

	numberWidth := len(strconv.Itoa(remote.AliasCount))
	aliases := make([]string, 0, remote.AliasCount)

	for aliasNumber := 1; aliasNumber <= remote.AliasCount; aliasNumber++ {
		alias := fmt.Sprintf(
			"%s-%0*d",
			remote.Alias,
			numberWidth,
			aliasNumber,
		)
		aliases = append(aliases, alias)
	}

	return aliases
}

// Fixture selects the data preset that will be created on a scenario target.
type Fixture struct {
	Target string        `yaml:"target"`
	Preset FixturePreset `yaml:"preset"`
	Name   string        `yaml:"name"`
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

	if !isSafeIdentifier(scenarioConfig.ID) {
		return fmt.Errorf("id cannot contain a path")
	}

	if scenarioConfig.Name == "" {
		return fmt.Errorf("name is required")
	}

	if len(scenarioConfig.Topology.Remotes) != 1 {
		return fmt.Errorf("topology.remotes must contain exactly one remote")
	}

	targets := map[string]bool{
		CoordinatorTarget: true,
	}

	for remoteIndex, remote := range scenarioConfig.Topology.Remotes {
		if remote.ID == "" {
			return fmt.Errorf("topology.remotes[%d].id is required", remoteIndex)
		}

		if remote.Alias == "" {
			return fmt.Errorf("topology.remotes[%d].alias is required", remoteIndex)
		}

		if remote.AliasCount < 0 {
			return fmt.Errorf("topology.remotes[%d].aliasCount cannot be negative", remoteIndex)
		}

		if !isSafeIdentifier(remote.ID) {
			return fmt.Errorf("topology.remotes[%d].id cannot contain a path", remoteIndex)
		}

		if remote.ID == CoordinatorTarget {
			return fmt.Errorf(
				"topology.remotes[%d].id cannot be %q",
				remoteIndex,
				CoordinatorTarget,
			)
		}

		if !isValidRemoteAlias(remote.Alias) {
			return fmt.Errorf(
				"topology.remotes[%d].alias can contain only letters, numbers, hyphens, and underscores",
				remoteIndex,
			)
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

		if fixture.Name == "" {
			return fmt.Errorf("fixtures[%d].name is required", fixtureIndex)
		}

		if !isSafeIdentifier(fixture.Name) {
			return fmt.Errorf("fixtures[%d].name cannot contain a path", fixtureIndex)
		}

		presetIsSupported := fixture.Preset == FixturePresetMinimalLogs ||
			fixture.Preset == FixturePresetMinimalLookup
		if !presetIsSupported {
			return fmt.Errorf(
				"fixtures[%d].preset %q is not supported",
				fixtureIndex,
				fixture.Preset,
			)
		}
	}

	faultIDs := make(map[string]bool, len(scenarioConfig.Faults))

	for faultIndex, fault := range scenarioConfig.Faults {
		if fault.ID == "" {
			return fmt.Errorf("faults[%d].id is required", faultIndex)
		}

		if faultIDs[fault.ID] {
			return fmt.Errorf("faults[%d].id %q is duplicated", faultIndex, fault.ID)
		}
		faultIDs[fault.ID] = true

		if !targets[fault.Target] {
			return fmt.Errorf(
				"faults[%d].target %q does not match a configured target",
				faultIndex,
				fault.Target,
			)
		}

		if fault.Target == CoordinatorTarget {
			return fmt.Errorf(
				"faults[%d].target must reference a remote cluster",
				faultIndex,
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

// isSafeIdentifier protects runtime paths without restricting ordinary scenario names.
func isSafeIdentifier(identifier string) bool {
	identifierIsPath := filepath.Base(identifier) != identifier

	return !identifierIsPath && identifier != "." && identifier != ".."
}

// isValidRemoteAlias keeps the alias safe for Elasticsearch cluster setting keys.
func isValidRemoteAlias(alias string) bool {
	for _, character := range alias {
		characterIsLetter := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z'
		characterIsNumber := character >= '0' && character <= '9'
		characterIsSeparator := character == '-' || character == '_'

		if !characterIsLetter && !characterIsNumber && !characterIsSeparator {
			return false
		}
	}

	return true
}
