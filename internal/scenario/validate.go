package scenario

import (
	"fmt"
	"regexp"
	"time"
)

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

	if !scenarioIDPattern.MatchString(scenarioConfig.ID) {
		return fmt.Errorf("id can contain only lowercase letters, numbers, and single underscores")
	}

	if scenarioConfig.Name == "" {
		return fmt.Errorf("name is required")
	}

	if elasticsearchError := validateElasticsearch(scenarioConfig.Elasticsearch); elasticsearchError != nil {
		return elasticsearchError
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

// runnerOwnedSettings are the Elasticsearch settings the runner decides for itself.
// A scenario that could replace them would move a node's address or its data
// directory out from under the code that connects the topology and resets it, and
// the failure would appear far from the line that caused it.
var runnerOwnedSettings = map[string]bool{
	"cluster.name":                         true,
	"cluster.initial_master_nodes":         true,
	"cluster.max_shards_per_node":          true,
	"node.name":                            true,
	"node.roles":                           true,
	"network.host":                         true,
	"network.bind_host":                    true,
	"network.publish_host":                 true,
	"discovery.type":                       true,
	"discovery.seed_hosts":                 true,
	"http.port":                            true,
	"http.host":                            true,
	"http.bind_host":                       true,
	"http.publish_host":                    true,
	"http.publish_port":                    true,
	"transport.port":                       true,
	"transport.host":                       true,
	"transport.bind_host":                  true,
	"transport.publish_host":               true,
	"transport.publish_port":               true,
	"path.home":                            true,
	"path.data":                            true,
	"path.logs":                            true,
	"xpack.security.enabled":               true,
	"xpack.security.http.ssl.enabled":      true,
	"xpack.security.transport.ssl.enabled": true,
}

// heapPattern accepts the sizes Elasticsearch accepts for -Xms and -Xmx.
var (
	heapPattern         = regexp.MustCompile(`^[1-9][0-9]*[mgMG]$`)
	scenarioIDPattern   = regexp.MustCompile(`^[a-z0-9]+(?:_[a-z0-9]+)*$`)
	identifierPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	resourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	settingNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
)

// validateElasticsearch rejects tuning that the runner cannot honour, so that a
// scenario never claims a limit it did not actually get.
func validateElasticsearch(elasticsearchConfig Elasticsearch) error {
	if elasticsearchConfig.Heap != "" && !heapPattern.MatchString(elasticsearchConfig.Heap) {
		return fmt.Errorf(
			"elasticsearch.heap %q must be a size such as 512m or 8g",
			elasticsearchConfig.Heap,
		)
	}

	for name, value := range elasticsearchConfig.Settings {
		if !settingNamePattern.MatchString(name) {
			return fmt.Errorf(
				"elasticsearch.settings name %q can contain only lowercase letters, numbers, dots, underscores, and hyphens",
				name,
			)
		}

		if runnerOwnedSettings[name] {
			return fmt.Errorf("elasticsearch.settings cannot set %q, the runner owns it", name)
		}

		if value == "" {
			return fmt.Errorf("elasticsearch.settings[%q] is empty", name)
		}
	}

	return nil
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

		if !identifierPattern.MatchString(remote.ID) {
			return nil, fmt.Errorf(
				"topology.remotes[%d].id can contain only lowercase letters, numbers, hyphens, and underscores",
				remoteIndex,
			)
		}

		if remote.ID == CoordinatorTarget {
			return nil, fmt.Errorf(
				"topology.remotes[%d].id cannot be %q",
				remoteIndex,
				CoordinatorTarget,
			)
		}

		if !identifierPattern.MatchString(remote.Alias) {
			return nil, fmt.Errorf(
				"topology.remotes[%d].alias can contain only lowercase letters, numbers, hyphens, and underscores",
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

		if !resourceNamePattern.MatchString(fixture.Name) || len(fixture.Name) > 255 {
			return fmt.Errorf(
				"fixtures[%d].name must be a lowercase Elasticsearch name up to 255 characters",
				fixtureIndex,
			)
		}

		presetIsSupported := fixture.Preset == FixturePresetMinimalLogs ||
			fixture.Preset == FixturePresetMinimalLookup ||
			fixture.Preset == FixturePresetBulkLogs ||
			fixture.Preset == FixturePresetBulkDocuments
		if !presetIsSupported {
			return fmt.Errorf(
				"fixtures[%d].preset %q is not supported",
				fixtureIndex,
				fixture.Preset,
			)
		}

		if knobError := validateFixtureKnobs(fixtureIndex, fixture); knobError != nil {
			return knobError
		}
	}

	return nil
}

// validateFixtureKnobs checks that a fixture asks only for the sizing its preset
// reads, so that a scenario never declares a number that silently does nothing.
func validateFixtureKnobs(fixtureIndex int, fixture Fixture) error {
	switch fixture.Preset {
	case FixturePresetBulkLogs:
		if fixture.Documents != 0 || fixture.FieldsPerDocument != 0 ||
			fixture.MessageBytes != 0 || fixture.TimeRange != "" {
			return fmt.Errorf(
				"fixtures[%d] document sizing applies to preset %q only",
				fixtureIndex,
				FixturePresetBulkDocuments,
			)
		}

		return validateBulkLogs(fixtureIndex, fixture)
	case FixturePresetBulkDocuments:
		if fixture.FieldsPerDocument < 0 || fixture.FieldsPerDocument > MaxFieldsPerDocument {
			return fmt.Errorf(
				"fixtures[%d].fieldsPerDocument must be between 0 and %d",
				fixtureIndex,
				MaxFieldsPerDocument,
			)
		}

		if fixture.MessageBytes < 0 || fixture.MessageBytes > MaxMessageBytes {
			return fmt.Errorf(
				"fixtures[%d].messageBytes must be between 0 and %d",
				fixtureIndex,
				MaxMessageBytes,
			)
		}

		if fixture.DataStreams != 0 || fixture.FieldsPerStream != 0 {
			return fmt.Errorf(
				"fixtures[%d].dataStreams and fixtures[%d].fieldsPerStream apply to preset %q only",
				fixtureIndex,
				fixtureIndex,
				FixturePresetBulkLogs,
			)
		}

		if fixture.Documents < 1 {
			return fmt.Errorf("fixtures[%d].documents must be at least 1", fixtureIndex)
		}

		if fixture.Documents > MaxBulkDocuments {
			return fmt.Errorf(
				"fixtures[%d].documents cannot exceed %d",
				fixtureIndex,
				MaxBulkDocuments,
			)
		}

		if fixture.TimeRange != "" {
			timeRange, parseError := time.ParseDuration(fixture.TimeRange)
			if parseError != nil || timeRange <= 0 {
				return fmt.Errorf(
					"fixtures[%d].timeRange %q must be a positive duration such as 24h",
					fixtureIndex,
					fixture.TimeRange,
				)
			}
		}

		return nil
	default:

		if fixture.DataStreams != 0 || fixture.FieldsPerStream != 0 ||
			fixture.Documents != 0 || fixture.FieldsPerDocument != 0 ||
			fixture.MessageBytes != 0 || fixture.TimeRange != "" {
			return fmt.Errorf(
				"fixtures[%d] sizing applies to the bulk presets only",
				fixtureIndex,
			)
		}

		return nil
	}
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
		if !identifierPattern.MatchString(fault.ID) {
			return fmt.Errorf(
				"faults[%d].id can contain only lowercase letters, numbers, hyphens, and underscores",
				faultIndex,
			)
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
