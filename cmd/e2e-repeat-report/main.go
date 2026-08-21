package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type testResult struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Elapsed float64 `json:"elapsed_seconds"`
}

type runSummary struct {
	RunID    string       `json:"run_id"`
	Passed   int          `json:"passed"`
	Failed   int          `json:"failed"`
	Skipped  int          `json:"skipped"`
	Duration float64      `json:"duration_seconds"`
	Tests    []testResult `json:"tests"`
}

type scenarioAggregate struct {
	Name       string  `json:"name"`
	Executions int     `json:"executions"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	P95Seconds float64 `json:"p95_seconds"`
	MaxSeconds float64 `json:"max_seconds"`
	durations  []float64
}

type repeatSummary struct {
	GeneratedAt        time.Time           `json:"generated_at"`
	Runs               int                 `json:"runs"`
	PassingRuns        int                 `json:"passing_runs"`
	FailingRuns        int                 `json:"failing_runs"`
	ScenarioExecutions int                 `json:"scenario_executions"`
	Scenarios          []scenarioAggregate `json:"scenarios"`
	FailedRunIDs       []string            `json:"failed_run_ids,omitempty"`
}

func main() {
	input := flag.String("input", "", "directory containing per-run artifact directories")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "-input is required")
		os.Exit(2)
	}
	summary, err := aggregate(*input)
	if err == nil {
		err = write(*input, summary)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("repeatability: runs=%d passing=%d failing=%d report=%s\n", summary.Runs, summary.PassingRuns, summary.FailingRuns, filepath.Join(*input, "repeat-summary.md"))
}

func aggregate(root string) (repeatSummary, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return repeatSummary{}, fmt.Errorf("read repeat directory: %w", err)
	}
	result := repeatSummary{GeneratedAt: time.Now().UTC()}
	byScenario := make(map[string]*scenarioAggregate)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(root, entry.Name(), "summary.json"))
		if err != nil {
			continue
		}
		var run runSummary
		if err := json.Unmarshal(payload, &run); err != nil {
			return repeatSummary{}, fmt.Errorf("decode %s summary: %w", entry.Name(), err)
		}
		result.Runs++
		if run.Failed == 0 {
			result.PassingRuns++
		} else {
			result.FailingRuns++
			result.FailedRunIDs = append(result.FailedRunIDs, run.RunID)
		}
		for _, test := range run.Tests {
			result.ScenarioExecutions++
			scenario := byScenario[test.Name]
			if scenario == nil {
				scenario = &scenarioAggregate{Name: test.Name}
				byScenario[test.Name] = scenario
			}
			scenario.Executions++
			scenario.durations = append(scenario.durations, test.Elapsed)
			if test.Status == "pass" {
				scenario.Passed++
			} else if test.Status == "fail" {
				scenario.Failed++
			}
		}
	}
	if result.Runs == 0 {
		return repeatSummary{}, fmt.Errorf("no run summaries found in %s", root)
	}
	for _, scenario := range byScenario {
		sort.Float64s(scenario.durations)
		scenario.MaxSeconds = scenario.durations[len(scenario.durations)-1]
		index := int(math.Ceil(float64(len(scenario.durations))*0.95)) - 1
		scenario.P95Seconds = scenario.durations[index]
		result.Scenarios = append(result.Scenarios, *scenario)
	}
	sort.Slice(result.Scenarios, func(i, j int) bool { return result.Scenarios[i].Name < result.Scenarios[j].Name })
	sort.Strings(result.FailedRunIDs)
	return result, nil
}

func write(root string, summary repeatSummary) error {
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "repeat-summary.json"), append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# E2E repeatability report\n\n- Runs: **%d**\n- Passing runs: **%d**\n- Failing runs: **%d**\n- Scenario executions: **%d**\n\n", summary.Runs, summary.PassingRuns, summary.FailingRuns, summary.ScenarioExecutions)
	report.WriteString("| Scenario | Executions | Passed | Failed | p95 | Max |\n| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, scenario := range summary.Scenarios {
		fmt.Fprintf(&report, "| `%s` | %d | %d | %d | %.2fs | %.2fs |\n", scenario.Name, scenario.Executions, scenario.Passed, scenario.Failed, scenario.P95Seconds, scenario.MaxSeconds)
	}
	if len(summary.FailedRunIDs) > 0 {
		report.WriteString("\n## Failed runs\n\n")
		for _, runID := range summary.FailedRunIDs {
			fmt.Fprintf(&report, "- `%s`\n", runID)
		}
	}
	return os.WriteFile(filepath.Join(root, "repeat-summary.md"), []byte(report.String()), 0o644)
}
