// Package scenarios exposes the standard scenario catalog embedded in the binary.
package scenarios

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed *.yml
var catalog embed.FS

// Read returns a standard scenario by its filename stem, with or without .yml.
func Read(name string) (string, []byte, error) {
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return "", nil, fmt.Errorf("invalid built-in scenario name %q", name)
	}

	filename := name
	if !strings.HasSuffix(filename, ".yml") {
		filename += ".yml"
	}

	contents, readError := catalog.ReadFile(filename)
	if readError != nil {
		return "", nil, fmt.Errorf("unknown built-in scenario %q", name)
	}

	return filename, contents, nil
}
