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

	// MaxRemoteAliases bounds aliasCount so every alias gets its own Toxiproxy
	// proxy inside the port range the runner reserves for them. The runner
	// allocates one port per alias starting at its base proxy port, so raising
	// this constant requires widening that range as well.
	MaxRemoteAliases = 100
)

// FixturePreset limits YAML values to data setups implemented by the runner.
type FixturePreset string

const (
	FixturePresetMinimalLogs   FixturePreset = "minimal_logs"
	FixturePresetMinimalLookup FixturePreset = "minimal_lookup"
	FixturePresetBulkLogs      FixturePreset = "bulk_logs"
)

const (
	// MaxBulkDataStreams keeps index creation, which is one cluster state update
	// each, within a startup time that still feels interactive.
	MaxBulkDataStreams = 2000

	// MaxFieldsPerStream stays under Elasticsearch's default
	// index.mapping.total_fields.limit of 1000, which the runner does not raise.
	MaxFieldsPerStream = 900
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
//
// DataStreams and FieldsPerStream apply to the bulk_logs preset only. They drive
// two different symptoms: the data stream count decides how much index resolution
// costs, because _resolve/index returns every data stream and backing index, while
// the field count decides how much _field_caps and Kibana's field list cost. A
// scenario that registers many CCS aliases multiplies both by the alias count.
type Fixture struct {
	Target          string        `yaml:"target"`
	Preset          FixturePreset `yaml:"preset"`
	Name            string        `yaml:"name"`
	DataStreams     int           `yaml:"dataStreams"`
	FieldsPerStream int           `yaml:"fieldsPerStream"`
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

		if remote.AliasCount > MaxRemoteAliases {
			return fmt.Errorf(
				"topology.remotes[%d].aliasCount cannot exceed %d, one proxy port is reserved per alias",
				remoteIndex,
				MaxRemoteAliases,
			)
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
			fixture.Preset == FixturePresetMinimalLookup ||
			fixture.Preset == FixturePresetBulkLogs
		if !presetIsSupported {
			return fmt.Errorf(
				"fixtures[%d].preset %q is not supported",
				fixtureIndex,
				fixture.Preset,
			)
		}

		if fixture.Preset == FixturePresetBulkLogs {
			if validationError := validateBulkLogs(fixtureIndex, fixture); validationError != nil {
				return validationError
			}

			continue
		}

		if fixture.DataStreams != 0 || fixture.FieldsPerStream != 0 {
			return fmt.Errorf(
				"fixtures[%d].dataStreams and fixtures[%d].fieldsPerStream apply to preset %q only",
				fixtureIndex,
				fixtureIndex,
				FixturePresetBulkLogs,
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

// validateBulkLogs keeps a fan-out fixture within what one local node can create.
func validateBulkLogs(fixtureIndex int, fixture Fixture) error {
	if fixture.DataStreams < 1 {
		return fmt.Errorf("fixtures[%d].dataStreams must be at least 1", fixtureIndex)
	}

	if fixture.DataStreams > MaxBulkDataStreams {
		return fmt.Errorf(
			"fixtures[%d].dataStreams cannot exceed %d",
			fixtureIndex,
			MaxBulkDataStreams,
		)
	}

	if fixture.FieldsPerStream < 1 {
		return fmt.Errorf("fixtures[%d].fieldsPerStream must be at least 1", fixtureIndex)
	}

	if fixture.FieldsPerStream > MaxFieldsPerStream {
		return fmt.Errorf(
			"fixtures[%d].fieldsPerStream cannot exceed %d, which is Elasticsearch's default field limit",
			fixtureIndex,
			MaxFieldsPerStream,
		)
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
