// Package assertions evaluates safety properties across independent system
// authorities rather than trusting one API response.
package assertions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot captures local workflow, durable publication, and DSS state at one
// observation time.
type Snapshot struct {
	ObservedAt            time.Time `json:"observed_at"`
	IntentID              string    `json:"intent_id"`
	LocalVersion          int       `json:"local_version"`
	LocalStatus           string    `json:"local_status"`
	DesiredVersion        int       `json:"desired_version,omitempty"`
	PublishedVersion      int       `json:"published_version,omitempty"`
	DesiredDSSState       string    `json:"desired_dss_state,omitempty"`
	ConfirmedDSSState     string    `json:"confirmed_dss_state,omitempty"`
	PublicationSyncStatus string    `json:"publication_sync_status,omitempty"`
	PublicationOVN        string    `json:"publication_ovn,omitempty"`
	DSSReferenceExists    bool      `json:"dss_reference_exists"`
	DSSVersion            int       `json:"dss_version,omitempty"`
	DSSState              string    `json:"dss_state,omitempty"`
	DSSOVN                string    `json:"dss_ovn,omitempty"`
}

// Result is one named invariant check suitable for an evidence report.
type Result struct {
	Name     string         `json:"name"`
	Passed   bool           `json:"passed"`
	Message  string         `json:"message"`
	Observed map[string]any `json:"observed,omitempty"`
}

// ActiveRequiresDSSActivated enforces the takeoff-authority boundary.
func ActiveRequiresDSSActivated(snapshot Snapshot) Result {
	result := Result{Name: "local_active_requires_same_version_dss_activated", Passed: true, Message: "local operation is not active"}
	if snapshot.LocalStatus != "active" {
		return result
	}
	result.Observed = map[string]any{
		"local_version": snapshot.LocalVersion, "published_version": snapshot.PublishedVersion,
		"confirmed_dss_state": snapshot.ConfirmedDSSState, "dss_state": snapshot.DSSState,
	}
	if snapshot.PublishedVersion == snapshot.LocalVersion && snapshot.ConfirmedDSSState == "Activated" &&
		snapshot.DSSReferenceExists && snapshot.DSSState == "Activated" {
		result.Message = "local Active is backed by the same DSS-confirmed Activated version"
		return result
	}
	result.Passed = false
	result.Message = fmt.Sprintf(
		"forbidden state: local intent v%d is Active while publication v%d is %q and DSS is %q",
		snapshot.LocalVersion, snapshot.PublishedVersion, snapshot.ConfirmedDSSState, snapshot.DSSState,
	)
	return result
}

// BlockedVersionHasNoDSSReference rejects publication of a desired version
// after durable coordination has declared it blocked.
func BlockedVersionHasNoDSSReference(snapshot Snapshot) Result {
	result := Result{Name: "blocked_version_has_no_dss_reference", Passed: true, Message: "publication is not blocked"}
	if snapshot.PublicationSyncStatus != "blocked" {
		return result
	}
	result.Observed = map[string]any{"desired_version": snapshot.DesiredVersion, "dss_version": snapshot.DSSVersion, "dss_state": snapshot.DSSState}
	if !snapshot.DSSReferenceExists || snapshot.DSSVersion < snapshot.DesiredVersion {
		result.Message = "blocked desired version is not published in the DSS"
		return result
	}
	result.Passed = false
	result.Message = fmt.Sprintf("blocked desired version %d is visible in DSS version %d", snapshot.DesiredVersion, snapshot.DSSVersion)
	return result
}

// DSSReference is the authoritative remote state used by invariant probes.
type DSSReference struct {
	Exists     bool
	Version    int
	State      string
	OVN        string
	Manager    string
	USSBaseURL string
}

// DSSReferenceObserver reads one operational-intent reference from a DSS
// authority without relying on the Aero Arc API's publication receipt.
type DSSReferenceObserver interface {
	ObserveDSSReference(context.Context, string) (DSSReference, error)
}

// Probe compares PostgreSQL state with an independently observed DSS reference.
type Probe struct {
	database *pgxpool.Pool
	dss      DSSReferenceObserver
	now      func() time.Time
}

// NewProbe connects to the authoritative local database and deterministic DSS
// fixture. Existing fixture-backed scenarios use this convenience constructor.
func NewProbe(ctx context.Context, databaseURL, dssControlURL string) (*Probe, error) {
	return NewProbeWithObserver(ctx, databaseURL, &fixtureDSSObserver{
		baseURL: strings.TrimRight(dssControlURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	})
}

// NewProbeWithObserver connects the invariant probe to a local database and a
// profile-specific DSS observer. The invariant functions and Snapshot contract
// remain identical across fixture and real-InterUSS profiles.
func NewProbeWithObserver(ctx context.Context, databaseURL string, dss DSSReferenceObserver) (*Probe, error) {
	if dss == nil {
		return nil, fmt.Errorf("DSS reference observer is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect invariant probe database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping invariant probe database: %w", err)
	}
	return &Probe{database: pool, dss: dss, now: time.Now}, nil
}

// Close releases the database pool.
func (probe *Probe) Close() {
	probe.database.Close()
}

// Snapshot observes all authorities for one intent.
func (probe *Probe) Snapshot(ctx context.Context, intentID string) (Snapshot, error) {
	snapshot := Snapshot{ObservedAt: probe.now().UTC(), IntentID: intentID}
	if err := probe.database.QueryRow(ctx, `
		SELECT version, data->>'status'
		FROM operational_intents
		WHERE id = $1
		ORDER BY version DESC LIMIT 1`, intentID).Scan(&snapshot.LocalVersion, &snapshot.LocalStatus); err != nil {
		return Snapshot{}, fmt.Errorf("query local intent: %w", err)
	}
	var confirmed *string
	var ovn *string
	err := probe.database.QueryRow(ctx, `
		SELECT desired_intent_version, COALESCE(published_intent_version, 0),
		       desired_state, sync_status, data->>'confirmed_state', data->>'ovn'
		FROM operational_intent_publications WHERE intent_id = $1`, intentID).Scan(
		&snapshot.DesiredVersion, &snapshot.PublishedVersion, &snapshot.DesiredDSSState,
		&snapshot.PublicationSyncStatus, &confirmed, &ovn,
	)
	if err == nil {
		if confirmed != nil {
			snapshot.ConfirmedDSSState = *confirmed
		}
		if ovn != nil {
			snapshot.PublicationOVN = *ovn
		}
	}
	reference, err := probe.dss.ObserveDSSReference(ctx, intentID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query DSS reference: %w", err)
	}
	snapshot.DSSReferenceExists = reference.Exists
	snapshot.DSSVersion = reference.Version
	snapshot.DSSState = reference.State
	snapshot.DSSOVN = reference.OVN
	return snapshot, nil
}

type fixtureDSSObserver struct {
	baseURL string
	http    *http.Client
}

func (observer *fixtureDSSObserver) ObserveDSSReference(ctx context.Context, intentID string) (DSSReference, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, observer.baseURL+"/control/state", nil)
	if err != nil {
		return DSSReference{}, err
	}
	response, err := observer.http.Do(request)
	if err != nil {
		return DSSReference{}, fmt.Errorf("query fixture state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DSSReference{}, fmt.Errorf("query fixture state returned %d", response.StatusCode)
	}
	var body struct {
		References []struct {
			ID         string `json:"id"`
			Version    int    `json:"version"`
			State      string `json:"state"`
			OVN        string `json:"ovn"`
			Manager    string `json:"manager"`
			USSBaseURL string `json:"uss_base_url"`
		} `json:"operational_intent_references"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return DSSReference{}, fmt.Errorf("decode fixture state: %w", err)
	}
	for _, reference := range body.References {
		if reference.ID != intentID {
			continue
		}
		return DSSReference{
			Exists: true, Version: reference.Version, State: reference.State,
			OVN: reference.OVN, Manager: reference.Manager, USSBaseURL: reference.USSBaseURL,
		}, nil
	}
	return DSSReference{}, nil
}

// Eventually samples the system until all checks pass or the context expires.
// Every sample is returned so reports can show transient forbidden states.
func (probe *Probe) Eventually(ctx context.Context, intentID string, interval time.Duration, checks ...func(Snapshot) Result) ([]Snapshot, []Result, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var samples []Snapshot
	var last []Result
	delay := interval
	for {
		snapshot, err := probe.Snapshot(ctx, intentID)
		if err == nil {
			samples = append(samples, snapshot)
			last = last[:0]
			passed := true
			for _, check := range checks {
				result := check(snapshot)
				last = append(last, result)
				passed = passed && result.Passed
			}
			if passed {
				return samples, append([]Result(nil), last...), nil
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return samples, append([]Result(nil), last...), fmt.Errorf("invariants did not converge: %w", ctx.Err())
		case <-timer.C:
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}
}
