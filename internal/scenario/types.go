package scenario

import (
	"fmt"
	"strconv"
)

const (
	supportedVersion   = 1
	LatencyFaultType   = "latency"
	BandwidthFaultType = "bandwidth"
	CoordinatorTarget  = "coordinator"
	// MaxRemoteAliases matches the proxy-port range reserved by the runner.
	MaxRemoteAliases = 400
)

// FixturePreset limits YAML values to data setups implemented by the runner.
type FixturePreset string

const (
	FixturePresetMinimalLogs       FixturePreset = "minimal_logs"
	FixturePresetMinimalLookup     FixturePreset = "minimal_lookup"
	FixturePresetBulkLogs          FixturePreset = "bulk_logs"
	FixturePresetBulkDocuments     FixturePreset = "bulk_documents"
	FixturePresetESArchive         FixturePreset = "es_archive"
	FixturePresetMultiplyDocuments FixturePreset = "multiply_documents"
)

const (
	// Fixture limits keep local setup bounded and within Elasticsearch defaults.
	MaxBulkDataStreams   = 2000
	MaxFieldsPerStream   = 900
	MaxBulkDocuments     = 5000000
	MaxFieldsPerDocument = 900
	MaxMessageBytes      = 65536
	MaxDocumentCopies    = 100
)

// Scenario describes the infrastructure, test data, and network faults in YAML.
type Scenario struct {
	Version       int           `yaml:"version"`
	ID            string        `yaml:"id"`
	Name          string        `yaml:"name"`
	Elasticsearch Elasticsearch `yaml:"elasticsearch"`
	Topology      Topology      `yaml:"topology"`
	Fixtures      []Fixture     `yaml:"fixtures"`
	Faults        []Fault       `yaml:"faults"`
}

// Elasticsearch contains scenario-specific heap and cluster tuning.
type Elasticsearch struct {
	Heap     string            `yaml:"heap"`
	Settings map[string]string `yaml:"settings"`
}

// Topology identifies the remote Elasticsearch clusters required by a scenario.
type Topology struct {
	Remotes []Remote `yaml:"remotes"`
}

// Remote describes one physical cluster and its logical CCS aliases.
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
		aliases = append(aliases, fmt.Sprintf("%s-%0*d", remote.Alias, numberWidth, aliasNumber))
	}

	return aliases
}

// Fixture selects a data preset and its size on one scenario target.
type Fixture struct {
	Target            string        `yaml:"target"`
	Preset            FixturePreset `yaml:"preset"`
	Name              string        `yaml:"name"`
	DataStreams       int           `yaml:"dataStreams"`
	FieldsPerStream   int           `yaml:"fieldsPerStream"`
	Documents         int           `yaml:"documents"`
	FieldsPerDocument int           `yaml:"fieldsPerDocument"`
	MessageBytes      int           `yaml:"messageBytes"`
	TimeRange         string        `yaml:"timeRange"`
	Archive           string        `yaml:"archive"`
	Source            string        `yaml:"source"`
	Copies            int           `yaml:"copies"`
}

// Fault describes a network degradation applied to a scenario target.
type Fault struct {
	ID            string `yaml:"id"`
	Target        string `yaml:"target"`
	AliasPosition int    `yaml:"aliasPosition"`
	Type          string `yaml:"type"`
	LatencyMS     int    `yaml:"latencyMs"`
	JitterMS      int    `yaml:"jitterMs"`
	RateKBps      int    `yaml:"rateKbps"`
}
