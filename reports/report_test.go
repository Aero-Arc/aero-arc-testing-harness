package reports

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndWriteBundle(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"example/e2e","Test":"TestFederation"}`,
		`{"Action":"run","Package":"example/e2e","Test":"TestFederation/happy"}`,
		`{"Action":"output","Package":"example/e2e","Test":"TestFederation/happy","Output":"checkpoint passed\n"}`,
		`{"Action":"pass","Package":"example/e2e","Test":"TestFederation/happy","Elapsed":1.25}`,
		`{"Action":"run","Package":"example/e2e","Test":"TestFederation/failure"}`,
		`{"Action":"fail","Package":"example/e2e","Test":"TestFederation/failure","Elapsed":0.5}`,
		`{"Action":"fail","Package":"example/e2e","Test":"TestFederation","Elapsed":1.75}`,
	}, "\n")
	summary, err := Parse(strings.NewReader(stream), "seed-42")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Passed != 1 || summary.Failed != 1 || len(summary.Tests) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	events, err := LoadTimeline(strings.NewReader(`{"scenario":"lost-response","phase":"then","name":"ovn recovered","status":"pass"}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	summary.Checkpoints = events
	directory := t.TempDir()
	if err := WriteBundle(directory, summary); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"summary.json", "summary.md", "junit.xml"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}
