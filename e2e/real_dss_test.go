//go:build realdss && !e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Aero-Arc/aero-arc-test-harness/assertions"
	"github.com/Aero-Arc/aero-arc-test-harness/environments/realdss"
	"github.com/Aero-Arc/aero-arc-test-harness/reports"
)

var (
	realEnvironment *realdss.Environment
	realTimeline    *reports.Timeline
)

func TestMain(m *testing.M) {
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	apiSource := valueOrDefault(os.Getenv("AERO_ARC_API_SOURCE"), filepath.Join(root, "..", "aero-arc-api"))
	dssSource := valueOrDefault(os.Getenv("AERO_ARC_INTERUSS_SOURCE"), filepath.Join(root, "..", "interuss-dss"))
	artifactDir := valueOrDefault(os.Getenv("AERO_ARC_E2E_ARTIFACT_DIR"), filepath.Join(root, "artifacts", "real-dss-manual"))
	seed, _ := strconv.ParseInt(os.Getenv("AERO_ARC_E2E_SEED"), 10, 64)
	if seed == 0 {
		seed = 1
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	realTimeline, err = reports.OpenTimeline(filepath.Join(artifactDir, "events.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	realEnvironment, err = realdss.Start(ctx, realdss.Config{
		RootDir: root, APISource: apiSource, InterUSSSource: dssSource,
		APISourceRevision: os.Getenv("AERO_ARC_API_REVISION"),
		DSSSourceRevision: os.Getenv("AERO_ARC_INTERUSS_REVISION"),
		ArtifactDir:       artifactDir, Seed: seed,
	})
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = realTimeline.Close()
		os.Exit(1)
	}
	code := m.Run()
	captureCtx, captureCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if code != 0 || os.Getenv("AERO_ARC_E2E_CAPTURE_ALL_LOGS") == "true" {
		_ = realEnvironment.Capture(captureCtx)
	}
	_ = realEnvironment.Close(captureCtx)
	captureCancel()
	_ = realTimeline.Close()
	os.Exit(code)
}

func TestRealFederation(t *testing.T) {
	t.Run("real_InterUSS_activation_and_withdrawal_preserve_invariant", func(t *testing.T) {
		intentID := "10000000-0000-4000-8000-000000000001"
		start := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
		realCreateSubmittedIntent(t, realEnvironment.USSA, intentID, start, 30.250, -97.760)
		realAcceptAndAwaitConfirmation(t, realEnvironment.USSA, intentID)

		active := realPostExpect(t, realEnvironment.USSA.APIBaseURL, "/api/v1/operational-intents/"+intentID+"/activate", nil, http.StatusOK)
		if active["status"] != "active" {
			t.Fatalf("USS-A activation status = %#v, want active", active["status"])
		}
		observer := realObserver(t, realEnvironment.USSA.Subject)
		probe, err := assertions.NewProbeWithObserver(context.Background(), realEnvironment.USSA.DatabaseURL, observer)
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		snapshot, err := probe.Snapshot(context.Background(), intentID)
		if err != nil {
			t.Fatal(err)
		}
		result := assertions.ActiveRequiresDSSActivated(snapshot)
		realAssertInvariant(t, intentID, result)
		reference, err := observer.ObserveDSSReference(context.Background(), intentID)
		if err != nil {
			t.Fatal(err)
		}
		if reference.Manager != "uss-a" || reference.USSBaseURL != "http://uss-a:8080" || reference.OVN == "" {
			t.Fatalf("real DSS reference does not identify USS-A authority: %#v", reference)
		}
		realRecord(t, "then", "real InterUSS reference matches independent USS-A", "pass", map[string]any{
			"manager": reference.Manager, "uss_base_url": reference.USSBaseURL,
			"dss_version": reference.Version, "ovn_present": reference.OVN != "",
		})

		realWithdrawAndAwait(t, realEnvironment.USSA, intentID)
		reference, err = observer.ObserveDSSReference(context.Background(), intentID)
		if err != nil {
			t.Fatal(err)
		}
		if reference.Exists {
			t.Fatalf("real DSS reference remains after confirmed withdrawal: %#v", reference)
		}
		realRecord(t, "then", "terminal USS-A state converged to real DSS withdrawal", "pass", nil)
	})

	t.Run("independent_USS_B_conflict_blocks_USS_A_publication", func(t *testing.T) {
		ussBIntentID := "20000000-0000-4000-8000-000000000002"
		ussAIntentID := "20000000-0000-4000-8000-000000000003"
		start := time.Now().UTC().Add(40 * time.Minute).Truncate(time.Second)
		realCreateSubmittedIntent(t, realEnvironment.USSB, ussBIntentID, start, 32.770, -96.800)
		realAcceptAndAwaitConfirmation(t, realEnvironment.USSB, ussBIntentID)
		observerB := realObserver(t, realEnvironment.USSB.Subject)
		peerReference, err := observerB.ObserveDSSReference(context.Background(), ussBIntentID)
		if err != nil {
			t.Fatal(err)
		}
		if !peerReference.Exists || peerReference.Manager != "uss-b" || peerReference.USSBaseURL != "http://uss-b:8080" {
			t.Fatalf("real DSS did not publish independent USS-B: %#v", peerReference)
		}
		realRecord(t, "given", "independent USS-B owns a real DSS reference", "pass", map[string]any{
			"intent_id": ussBIntentID, "manager": peerReference.Manager, "uss_base_url": peerReference.USSBaseURL,
		})

		realCreateSubmittedIntent(t, realEnvironment.USSA, ussAIntentID, start, 32.770, -96.800)
		check := realPostExpect(t, realEnvironment.USSA.APIBaseURL, "/api/v1/operational-intents/"+ussAIntentID+"/deconfliction/check", nil, http.StatusOK)
		if check["posture"] != "potential_conflict" {
			t.Fatalf("USS-A deconfliction posture = %#v, want potential_conflict", check["posture"])
		}
		realAssertPeerFinding(t, realEnvironment.USSA.DatabaseURL, ussAIntentID, ussBIntentID)

		realPostExpect(t, realEnvironment.USSA.APIBaseURL, "/api/v1/operational-intents/"+ussAIntentID+"/accept", nil, http.StatusOK)
		realAwaitCoordination(t, realEnvironment.USSA, ussAIntentID, 30*time.Second, func(value map[string]any) bool {
			return value["sync_status"] == "blocked"
		})
		realPostExpect(t, realEnvironment.USSA.APIBaseURL, "/api/v1/operational-intents/"+ussAIntentID+"/activate", nil, http.StatusConflict)
		realAssertLocalStatus(t, realEnvironment.USSA.DatabaseURL, ussAIntentID, "accepted")

		observerA := realObserver(t, realEnvironment.USSA.Subject)
		probe, err := assertions.NewProbeWithObserver(context.Background(), realEnvironment.USSA.DatabaseURL, observerA)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := probe.Snapshot(context.Background(), ussAIntentID)
		probe.Close()
		if err != nil {
			t.Fatal(err)
		}
		realAssertInvariant(t, ussAIntentID, assertions.BlockedVersionHasNoDSSReference(snapshot))
		if snapshot.DSSReferenceExists {
			t.Fatalf("blocked USS-A version is visible in the real DSS: %#v", snapshot)
		}
		realRecord(t, "then", "USS-B details blocked USS-A publication without a false clear", "pass", map[string]any{
			"posture": check["posture"], "uss_b_reference_exists": peerReference.Exists,
			"uss_a_reference_exists": snapshot.DSSReferenceExists,
		})

		realWithdrawAndAwait(t, realEnvironment.USSA, ussAIntentID)
		realWithdrawAndAwait(t, realEnvironment.USSB, ussBIntentID)
	})
}

func realCreateSubmittedIntent(t *testing.T, participant realdss.Participant, intentID string, start time.Time, lat, lon float64) {
	t.Helper()
	end := start.Add(40 * time.Minute)
	realPostExpect(t, participant.APIBaseURL, "/api/v1/operational-intents", map[string]any{
		"id": intentID, "aircraft_id": "aircraft-hawk-2", "name": "Real federation operation",
		"summary": "Real InterUSS and independent Aero Arc federation", "planned_start_at": start, "planned_end_at": end,
	}, http.StatusCreated)
	coordinates := fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f]]]}`,
		lon, lat, lon+0.01, lat, lon+0.01, lat+0.01, lon, lat+0.01, lon, lat)
	realPostExpect(t, participant.APIBaseURL, "/api/v1/operational-intents/"+intentID+"/volumes", map[string]any{
		"id": intentID + "-volume", "sequence": 1, "geojson": coordinates,
		"min_altitude_m": 30, "max_altitude_m": 90, "altitude_ref": "wgs84",
		"starts_at": start.Add(time.Minute), "ends_at": end.Add(-time.Minute), "volume_type": "route",
	}, http.StatusCreated)
	realPostExpect(t, participant.APIBaseURL, "/api/v1/operational-intents/"+intentID+"/submit", nil, http.StatusOK)
	realRecord(t, "given", participant.Name+" has a submitted operation", "pass", map[string]any{"intent_id": intentID})
}

func realAcceptAndAwaitConfirmation(t *testing.T, participant realdss.Participant, intentID string) map[string]any {
	t.Helper()
	realPostExpect(t, participant.APIBaseURL, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
	coordination := realAwaitCoordination(t, participant, intentID, 30*time.Second, func(value map[string]any) bool {
		return value["sync_status"] == "confirmed" && value["confirmed_state"] == "Accepted" && value["ovn"] != ""
	})
	realRecord(t, "when", participant.Name+" publication confirmed by real InterUSS", "pass", map[string]any{
		"intent_id": intentID, "ovn_present": coordination["ovn"] != "",
	})
	return coordination
}

func realWithdrawAndAwait(t *testing.T, participant realdss.Participant, intentID string) {
	t.Helper()
	realPostExpect(t, participant.APIBaseURL, "/api/v1/operational-intents/"+intentID+"/cancel", nil, http.StatusOK)
	realAwaitCoordination(t, participant, intentID, 30*time.Second, func(value map[string]any) bool {
		return value["sync_status"] == "withdrawn" && value["desired_state"] == "Withdrawn"
	})
}

func realAwaitCoordination(t *testing.T, participant realdss.Participant, intentID string, timeout time.Duration, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	delay := 50 * time.Millisecond
	var last map[string]any
	lastTransition := ""
	for time.Now().Before(deadline) {
		status, value := realGetJSON(t, participant.APIBaseURL+"/api/v1/operational-intents/"+intentID+"/coordination")
		if status == http.StatusOK {
			last = value
			transition := fmt.Sprintf("%v|%v|%v|%v|%v", value["sync_status"], value["desired_state"], value["confirmed_state"], value["attempt_count"], value["last_error"])
			if transition != lastTransition {
				realRecord(t, "observe", participant.Name+" coordination state transition", "observed", map[string]any{
					"intent_id": intentID, "sync_status": value["sync_status"], "desired_state": value["desired_state"],
					"confirmed_state": value["confirmed_state"], "attempt_count": value["attempt_count"], "last_error": value["last_error"],
				})
				lastTransition = transition
			}
			if ready(value) {
				return value
			}
		}
		time.Sleep(delay)
		if delay < 500*time.Millisecond {
			delay = time.Duration(float64(delay) * 1.5)
			if delay > 500*time.Millisecond {
				delay = 500 * time.Millisecond
			}
		}
	}
	t.Fatalf("%s coordination did not converge for %s: %#v", participant.Name, intentID, last)
	return nil
}

func realAssertPeerFinding(t *testing.T, databaseURL, intentID, peerIntentID string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var findings int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM conflict_findings
		WHERE intent_id = $1
		  AND data->>'status' = 'potential_conflict'
		  AND data->>'conflicting_intent_id' = $2
		  AND data->>'provenance' = 'interuss_scd|uss-b|http://uss-b:8080'`, intentID, peerIntentID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if findings == 0 {
		t.Fatal("USS-A did not persist a conflict finding sourced from USS-B details")
	}
	realRecord(t, "then", "USS-A persisted evidence from the authenticated USS-B detail route", "pass", map[string]any{
		"intent_id": intentID, "peer_intent_id": peerIntentID, "findings": findings,
	})
}

func realAssertLocalStatus(t *testing.T, databaseURL, intentID, expected string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT data->>'status' FROM operational_intents
		WHERE id = $1 ORDER BY version DESC LIMIT 1`, intentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != expected {
		t.Fatalf("local status = %q, want %q", status, expected)
	}
}

func realObserver(t *testing.T, subject string) *realdss.ReferenceObserver {
	t.Helper()
	observer, err := realEnvironment.Observer(subject)
	if err != nil {
		t.Fatal(err)
	}
	return observer
}

func realAssertInvariant(t *testing.T, intentID string, result assertions.Result) {
	t.Helper()
	status := "pass"
	if !result.Passed {
		status = "fail"
	}
	realRecord(t, "then", result.Name, status, map[string]any{
		"intent_id": intentID, "message": result.Message, "observed": result.Observed,
	})
	if !result.Passed {
		t.Fatal(result.Message)
	}
}

func realRecord(t *testing.T, phase, name, status string, details map[string]any) {
	t.Helper()
	if err := realTimeline.Record(reports.TimelineEvent{
		Scenario: t.Name(), Phase: phase, Name: name, Status: status, Details: details,
	}); err != nil {
		t.Fatalf("record real-DSS timeline checkpoint: %v", err)
	}
}

func realPostExpect(t *testing.T, baseURL, path string, body any, expected int) map[string]any {
	t.Helper()
	var payload io.Reader
	if body != nil {
		var buffer bytes.Buffer
		if err := json.NewEncoder(&buffer).Encode(body); err != nil {
			t.Fatal(err)
		}
		payload = &buffer
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != expected {
		t.Fatalf("POST %s status = %d, want %d: %s", path, response.StatusCode, expected, strings.TrimSpace(string(data)))
	}
	if len(data) == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode POST %s: %v: %s", path, err, data)
	}
	return value
}

func realGetJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var value map[string]any
	_ = json.NewDecoder(response.Body).Decode(&value)
	return response.StatusCode, value
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
