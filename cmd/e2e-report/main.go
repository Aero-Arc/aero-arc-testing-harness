package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Aero-Arc/aero-arc-test-harness/reports"
)

func main() {
	input := flag.String("input", "", "path to go test -json output")
	output := flag.String("output", "", "artifact directory")
	runID := flag.String("run-id", "", "stable run identifier")
	flag.Parse()
	if *input == "" || *output == "" || *runID == "" {
		fmt.Fprintln(os.Stderr, "-input, -output, and -run-id are required")
		os.Exit(2)
	}
	file, err := os.Open(filepath.Clean(*input))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	summary, err := reports.Parse(file, *runID)
	if err == nil {
		eventsPath := filepath.Join(*output, "events.jsonl")
		if eventsFile, openErr := os.Open(eventsPath); openErr == nil {
			summary.Checkpoints, err = reports.LoadTimeline(eventsFile)
			_ = eventsFile.Close()
		}
	}
	if err == nil {
		err = reports.WriteBundle(*output, summary)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("PASS=%d FAIL=%d SKIP=%d report=%s\n", summary.Passed, summary.Failed, summary.Skipped, *output)
}
