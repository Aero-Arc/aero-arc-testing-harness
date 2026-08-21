// Package scenarios provides a story-oriented runner for federation tests.
package scenarios

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Aero-Arc/aero-arc-test-harness/assertions"
	"github.com/Aero-Arc/aero-arc-test-harness/reports"
)

// Action drives the environment to a checkpoint and optionally returns cleanup
// that must execute even when a later assertion fails.
type Action func(context.Context) (cleanup func(context.Context) error, err error)

// Check evaluates one named system invariant.
type Check func(context.Context) (assertions.Result, error)

// Step is a readable Given/When action.
type Step struct {
	Name   string
	Action Action
}

// Assertion is a readable Then property.
type Assertion struct {
	Name  string
	Check Check
}

// Scenario describes a federation story without exposing the injection tool.
type Scenario struct {
	Name        string
	Description string
	Timeout     time.Duration
	Given       []Step
	When        []Step
	Then        []Assertion
}

// Runner executes scenarios and emits evidence at every checkpoint.
type Runner struct {
	Timeline *reports.Timeline
}

// Run executes Given, When, and Then in order and always runs cleanup in
// reverse order.
func (runner Runner) Run(parent context.Context, scenario Scenario) (err error) {
	if scenario.Name == "" || runner.Timeline == nil {
		return fmt.Errorf("scenario name and timeline are required")
	}
	timeout := scenario.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var cleanups []func(context.Context) error
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for index := len(cleanups) - 1; index >= 0; index-- {
			if cleanupErr := cleanups[index](cleanupCtx); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("scenario cleanup: %w", cleanupErr))
			}
		}
	}()
	if err := runner.runSteps(ctx, scenario.Name, "given", scenario.Given, &cleanups); err != nil {
		return err
	}
	if err := runner.runSteps(ctx, scenario.Name, "when", scenario.When, &cleanups); err != nil {
		return err
	}
	for _, assertion := range scenario.Then {
		result, checkErr := assertion.Check(ctx)
		status := "pass"
		if checkErr != nil || !result.Passed {
			status = "fail"
		}
		_ = runner.Timeline.Record(reports.TimelineEvent{
			Scenario: scenario.Name, Phase: "then", Name: assertion.Name, Status: status,
			Details: map[string]any{"result": result, "error": errorString(checkErr)},
		})
		if checkErr != nil {
			return fmt.Errorf("assert %s: %w", assertion.Name, checkErr)
		}
		if !result.Passed {
			return fmt.Errorf("assert %s: %s", assertion.Name, result.Message)
		}
	}
	return nil
}

func (runner Runner) runSteps(ctx context.Context, scenario, phase string, steps []Step, cleanups *[]func(context.Context) error) error {
	for _, step := range steps {
		cleanup, err := step.Action(ctx)
		status := "pass"
		if err != nil {
			status = "fail"
		}
		_ = runner.Timeline.Record(reports.TimelineEvent{
			Scenario: scenario, Phase: phase, Name: step.Name, Status: status,
			Details: map[string]any{"error": errorString(err)},
		})
		if cleanup != nil {
			*cleanups = append(*cleanups, cleanup)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", phase, step.Name, err)
		}
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
