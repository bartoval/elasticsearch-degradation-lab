package scenarios

import "testing"

func TestReadAcceptsScenarioNameWithOrWithoutExtension(t *testing.T) {
	for _, name := range []string{"local__rows500k", "local__rows500k.yml"} {
		filename, contents, readError := Read(name)
		if readError != nil {
			t.Fatalf("read %q: %v", name, readError)
		}
		if filename != "local__rows500k.yml" {
			t.Fatalf("filename = %q, want local__rows500k.yml", filename)
		}
		if len(contents) == 0 {
			t.Fatalf("contents for %q are empty", name)
		}
	}
}

func TestReadRejectsUnknownScenario(t *testing.T) {
	if _, _, readError := Read("local__missing"); readError == nil {
		t.Fatal("expected unknown scenario error")
	}
}
