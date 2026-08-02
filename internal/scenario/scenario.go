package scenario

import (
	"fmt"
	"path/filepath"
	"strconv"
)

const (
	supportedVersion   = 1
	LatencyFaultType   = "latency"
	BandwidthFaultType = "bandwidth"
	CoordinatorTarget  = "coordinator"

	// MaxRemoteAliases bounds aliasCount so every alias gets its own Toxiproxy
	// proxy inside the port range the runner reserves for them. The runner
	// allocates one port per alias starting at its base proxy port, so raising
	// this constant requires widening that range as well.
	MaxRemoteAliases = 400
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
// DataStreams and FieldsPerStream apply to the bulk_logs preset only. The first
// sets what index resolution has to return, the second what field capabilities do.
// DataStreams grows both index resolution and field capabilities. AliasCount also
// grows cross-cluster index resolution because every result is qualified by its
// cluster alias, while FieldsPerStream grows only the field-capabilities result.
type Fixture struct {
	Target          string        `yaml:"target"`
	Preset          FixturePreset `yaml:"preset"`
	Name            string        `yaml:"name"`
	DataStreams     int           `yaml:"dataStreams"`
	FieldsPerStream int           `yaml:"fieldsPerStream"`
}

// Fault describes a network degradation applied to a scenario target.
//
// The two types cost differently. Latency costs the same whatever the response
// weighs, so it models a distant remote. Bandwidth costs in proportion to the bytes
// crossing the proxy, which ties a measurement to what the fixture built, and is
// capped per connection rather than across them. Those bytes are the ones on the
// cross-cluster transport, not the ones in the response Elasticsearch assembles, so
// a rate cannot be derived from a response size: calibrate against the byte counters
// the proxy itself reports. AliasPosition is 1-based and selects one logical CCS
// alias; zero leaves the fault on every proxy belonging to the target.
type Fault struct {
	ID            string `yaml:"id"`
	Target        string `yaml:"target"`
	AliasPosition int    `yaml:"aliasPosition"`
	Type          string `yaml:"type"`
	LatencyMS     int    `yaml:"latencyMs"`
	JitterMS      int    `yaml:"jitterMs"`
	RateKBps      int    `yaml:"rateKbps"`
}

// Validate rejects scenarios that cannot be executed predictably by the current
// runner. It stops at the first problem, since a scenario is written by hand and
// one clear message is easier to act on than a list.
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

	// Every later section refers to a target by name, so the set of legal names has
	// to be established before any of them can be checked.
	targets, remoteError := validateRemotes(scenarioConfig.Topology.Remotes)
	if remoteError != nil {
		return remoteError
	}

	if fixtureError := validateFixtures(scenarioConfig.Fixtures, targets); fixtureError != nil {
		return fixtureError
	}

	return validateFaults(scenarioConfig.Faults, scenarioConfig.Topology.Remotes, targets)
}

// validateRemotes checks the topology and returns the target names a fixture or a
// fault is then allowed to refer to.
func validateRemotes(remotes []Remote) (map[string]bool, error) {
	if len(remotes) > 1 {
		return nil, fmt.Errorf("topology.remotes cannot contain more than one physical remote")
	}

	targets := map[string]bool{
		CoordinatorTarget: true,
	}

	for remoteIndex, remote := range remotes {
		if remote.ID == "" {
			return nil, fmt.Errorf("topology.remotes[%d].id is required", remoteIndex)
		}

		if remote.Alias == "" {
			return nil, fmt.Errorf("topology.remotes[%d].alias is required", remoteIndex)
		}

		if remote.AliasCount < 0 {
			return nil, fmt.Errorf("topology.remotes[%d].aliasCount cannot be negative", remoteIndex)
		}

		if remote.AliasCount > MaxRemoteAliases {
			return nil, fmt.Errorf(
				"topology.remotes[%d].aliasCount cannot exceed %d, one proxy port is reserved per alias",
				remoteIndex,
				MaxRemoteAliases,
			)
		}

		if !isSafeIdentifier(remote.ID) {
			return nil, fmt.Errorf("topology.remotes[%d].id cannot contain a path", remoteIndex)
		}

		if remote.ID == CoordinatorTarget {
			return nil, fmt.Errorf(
				"topology.remotes[%d].id cannot be %q",
				remoteIndex,
				CoordinatorTarget,
			)
		}

		if !isValidRemoteAlias(remote.Alias) {
			return nil, fmt.Errorf(
				"topology.remotes[%d].alias can contain only letters, numbers, hyphens, and underscores",
				remoteIndex,
			)
		}

		if targets[remote.ID] {
			return nil, fmt.Errorf("topology.remotes[%d].id %q is duplicated", remoteIndex, remote.ID)
		}

		targets[remote.ID] = true
	}

	return targets, nil
}

// validateFixtures rejects data setups the runner cannot create, and knobs asked of
// a preset that does not read them.
func validateFixtures(fixtures []Fixture, targets map[string]bool) error {
	for fixtureIndex, fixture := range fixtures {
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

	return nil
}

// validateFaults rejects degradations that name an unknown target, or that mix the
// settings of the two fault types, since a scenario that silently ignored half of
// what it declared would be measured with confidence and be wrong.
func validateFaults(faults []Fault, remotes []Remote, targets map[string]bool) error {
	faultIDs := make(map[string]bool, len(faults))
	aliasCountsByTarget := make(map[string]int, len(remotes))

	for _, remote := range remotes {
		aliasCountsByTarget[remote.ID] = len(remote.Aliases())
	}

	for faultIndex, fault := range faults {
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

		if fault.AliasPosition < 0 {
			return fmt.Errorf("faults[%d].aliasPosition cannot be negative", faultIndex)
		}

		if fault.AliasPosition > 0 {
			if fault.Target == CoordinatorTarget {
				return fmt.Errorf(
					"faults[%d].aliasPosition applies to remote targets only",
					faultIndex,
				)
			}

			aliasCount := aliasCountsByTarget[fault.Target]
			if fault.AliasPosition > aliasCount {
				return fmt.Errorf(
					"faults[%d].aliasPosition cannot exceed the %d aliases configured for target %q",
					faultIndex,
					aliasCount,
					fault.Target,
				)
			}
		}

		switch fault.Type {
		case LatencyFaultType:
			if fault.LatencyMS <= 0 {
				return fmt.Errorf("faults[%d].latencyMs must be greater than zero", faultIndex)
			}

			if fault.JitterMS < 0 {
				return fmt.Errorf("faults[%d].jitterMs cannot be negative", faultIndex)
			}

			if fault.RateKBps != 0 {
				return fmt.Errorf(
					"faults[%d].rateKbps applies to type %q only",
					faultIndex,
					BandwidthFaultType,
				)
			}
		case BandwidthFaultType:
			if fault.RateKBps <= 0 {
				return fmt.Errorf("faults[%d].rateKbps must be greater than zero", faultIndex)
			}

			if fault.LatencyMS != 0 || fault.JitterMS != 0 {
				return fmt.Errorf(
					"faults[%d].latencyMs and faults[%d].jitterMs apply to type %q only",
					faultIndex,
					faultIndex,
					LatencyFaultType,
				)
			}
		default:
			return fmt.Errorf(
				"faults[%d].type must be %q or %q",
				faultIndex,
				LatencyFaultType,
				BandwidthFaultType,
			)
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
