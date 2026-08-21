// Package reports converts raw Go test events into a compact evidence bundle.
package reports

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TestResult is the final status and diagnostic output for one test.
type TestResult struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Elapsed float64 `json:"elapsed_seconds"`
	Output  string  `json:"output,omitempty"`
}

// Summary is the digestible result of one E2E run.
type Summary struct {
	RunID       string          `json:"run_id"`
	GeneratedAt time.Time       `json:"generated_at"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	Skipped     int             `json:"skipped"`
	Duration    float64         `json:"duration_seconds"`
	Tests       []TestResult    `json:"tests"`
	Checkpoints []TimelineEvent `json:"checkpoints,omitempty"`
}

type goTestEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

// Parse reads the JSON stream emitted by `go test -json`.
func Parse(reader io.Reader, runID string) (Summary, error) {
	summary := Summary{RunID: runID, GeneratedAt: time.Now().UTC()}
	results := make(map[string]*TestResult)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return Summary{}, fmt.Errorf("decode go test event: %w", err)
		}
		if event.Test == "" {
			if event.Elapsed > summary.Duration {
				summary.Duration = event.Elapsed
			}
			continue
		}
		key := event.Package + "\x00" + event.Test
		result := results[key]
		if result == nil {
			result = &TestResult{Package: event.Package, Name: event.Test, Status: "running"}
			results[key] = result
		}
		if event.Output != "" {
			result.Output += event.Output
		}
		switch event.Action {
		case "pass", "fail", "skip":
			result.Status = event.Action
			result.Elapsed = event.Elapsed
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, fmt.Errorf("read go test events: %w", err)
	}
	// Go emits terminal events for both leaf subtests and their aggregate
	// parents. Reports count executable scenarios, not the parent container.
	parents := make(map[string]bool)
	for key, candidate := range results {
		for otherKey, other := range results {
			if key != otherKey && candidate.Package == other.Package && strings.HasPrefix(other.Name, candidate.Name+"/") {
				parents[key] = true
				break
			}
		}
	}
	for key, result := range results {
		if parents[key] {
			continue
		}
		if result.Status == "running" {
			result.Status = "fail"
			result.Output += "test stream ended without a terminal event\n"
		}
		summary.Tests = append(summary.Tests, *result)
		switch result.Status {
		case "pass":
			summary.Passed++
		case "skip":
			summary.Skipped++
		default:
			summary.Failed++
		}
	}
	sort.Slice(summary.Tests, func(i, j int) bool {
		if summary.Tests[i].Package == summary.Tests[j].Package {
			return summary.Tests[i].Name < summary.Tests[j].Name
		}
		return summary.Tests[i].Package < summary.Tests[j].Package
	})
	return summary, nil
}

// WriteBundle writes JSON, Markdown, and JUnit summaries atomically enough for
// a CI artifact directory owned by one run.
func WriteBundle(directory string, summary Summary) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	jsonBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "summary.json"), append(jsonBytes, '\n'), 0o644); err != nil {
		return fmt.Errorf("write JSON summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "summary.md"), []byte(markdown(summary)), 0o644); err != nil {
		return fmt.Errorf("write Markdown summary: %w", err)
	}
	junitBytes, err := xml.MarshalIndent(toJUnit(summary), "", "  ")
	if err != nil {
		return fmt.Errorf("encode JUnit summary: %w", err)
	}
	junitBytes = append([]byte(xml.Header), append(junitBytes, '\n')...)
	if err := os.WriteFile(filepath.Join(directory, "junit.xml"), junitBytes, 0o644); err != nil {
		return fmt.Errorf("write JUnit summary: %w", err)
	}
	return nil
}

// LoadTimeline reads JSONL checkpoints emitted by the scenario runner.
func LoadTimeline(reader io.Reader) ([]TimelineEvent, error) {
	var events []TimelineEvent
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event TimelineEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode timeline event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read timeline: %w", err)
	}
	return events, nil
}

func markdown(summary Summary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Aero Arc E2E run `%s`\n\n", summary.RunID)
	fmt.Fprintf(&builder, "- Passed: **%d**\n- Failed: **%d**\n- Skipped: **%d**\n- Duration: **%.2fs**\n\n", summary.Passed, summary.Failed, summary.Skipped, summary.Duration)
	builder.WriteString("| Scenario | Result | Duration |\n| --- | --- | ---: |\n")
	for _, result := range summary.Tests {
		fmt.Fprintf(&builder, "| `%s` | %s | %.2fs |\n", result.Name, strings.ToUpper(result.Status), result.Elapsed)
	}
	if len(summary.Checkpoints) > 0 {
		builder.WriteString("\n## Checkpoints and invariants\n\n| Time (UTC) | Scenario | Phase | Checkpoint | Result | Evidence |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, event := range summary.Checkpoints {
			fmt.Fprintf(&builder, "| %s | `%s` | %s | %s | %s | %s |\n",
				event.At.UTC().Format("15:04:05.000"), event.Scenario, event.Phase,
				event.Name, strings.ToUpper(event.Status), markdownDetails(event.Details))
		}
	}
	for _, result := range summary.Tests {
		if result.Status != "fail" {
			continue
		}
		fmt.Fprintf(&builder, "\n## Failure: `%s`\n\n```text\n%s\n```\n", result.Name, strings.TrimSpace(result.Output))
	}
	builder.WriteString("\n## Artifact index\n\n")
	builder.WriteString("- [Machine-readable summary](summary.json)\n")
	builder.WriteString("- [Chronological evidence](events.jsonl)\n")
	builder.WriteString("- [Raw Go test stream](go-test.json)\n")
	builder.WriteString("- [Environment manifest](stack.json)\n")
	builder.WriteString("- [JUnit report](junit.xml)\n")
	builder.WriteString("- [Service logs](logs/) — captured automatically on failure\n")
	return builder.String()
}

func markdownDetails(details map[string]any) string {
	if len(details) == 0 {
		return "—"
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return "details unavailable"
	}
	value := strings.ReplaceAll(string(encoded), "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return "`" + strings.ReplaceAll(value, "`", "'") + "`"
}

type junitSuites struct {
	XMLName  xml.Name   `xml:"testsuites"`
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Skipped  int        `xml:"skipped,attr"`
	Time     string     `xml:"time,attr"`
	Suite    junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Skipped   int         `xml:"skipped,attr"`
	Time      string      `xml:"time,attr"`
	TestCases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Skipped   *struct{}     `xml:"skipped,omitempty"`
	Output    string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func toJUnit(summary Summary) junitSuites {
	cases := make([]junitCase, 0, len(summary.Tests))
	for _, result := range summary.Tests {
		item := junitCase{Name: result.Name, ClassName: result.Package, Time: fmt.Sprintf("%.3f", result.Elapsed), Output: result.Output}
		switch result.Status {
		case "fail":
			item.Failure = &junitFailure{Message: "scenario failed", Body: result.Output}
		case "skip":
			item.Skipped = &struct{}{}
		}
		cases = append(cases, item)
	}
	suite := junitSuite{
		Name: "aero-arc-e2e", Tests: len(cases), Failures: summary.Failed, Skipped: summary.Skipped,
		Time: fmt.Sprintf("%.3f", summary.Duration), TestCases: cases,
	}
	return junitSuites{
		Name: summary.RunID, Tests: len(cases), Failures: summary.Failed, Skipped: summary.Skipped,
		Time: fmt.Sprintf("%.3f", summary.Duration), Suite: suite,
	}
}
