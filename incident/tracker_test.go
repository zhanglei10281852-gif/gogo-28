package incident

import (
	"strings"
	"testing"
	"time"

	"firescope/geo"
)

func testConfig() Config {
	config := DefaultConfig()
	config.AssociationDistance = 20_000
	config.AssociationWindow = time.Hour
	config.ActivationEvidence = 2
	config.ActivationSources = 2
	config.CandidateSilence = 30 * time.Minute
	config.ContainmentSilence = time.Hour
	config.ClosureSilence = 2 * time.Hour
	config.MaximumDuration = 24 * time.Hour
	config.ReopenWindow = 3 * time.Hour
	config.ReopenDistance = 25_000
	return config
}

func candidate(id, source string, at time.Time, lat, lon, frp, confidence float64) Candidate {
	return Candidate{EvidenceID: id, Source: source, ObservedAt: at, Location: geo.Point{Lat: lat, Lon: lon}, FRP: frp, Confidence: confidence}
}

func TestTrackerLifecycleAndReappearance(t *testing.T) {
	base := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	tracker, err := NewTracker(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, err := tracker.Ingest(candidate("a", "sat-a", base, 10, 20, 12, 60))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != CandidateStatus || !strings.HasPrefix(first.ID, "inc-") {
		t.Fatalf("unexpected initial incident: %+v", first)
	}
	active, err := tracker.Ingest(candidate("b", "sat-b", base.Add(10*time.Minute), 10.01, 20.01, 30, 90))
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID || active.Status != ActiveStatus {
		t.Fatalf("evidence did not activate same incident: %+v", active)
	}
	if active.FRPPeak != 30 || active.Confidence != 90 || len(active.Evidence) != 2 || len(active.Sources) != 2 {
		t.Fatalf("aggregates not updated: %+v", active)
	}
	if _, err := tracker.Transition(active.ID, CandidateStatus, base.Add(20*time.Minute), "invalid regression"); err == nil {
		t.Fatal("expected illegal transition error")
	}
	changed, err := tracker.Advance(base.Add(90 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Status != ContainedStatus {
		t.Fatalf("expected containment, got %+v", changed)
	}
	late, err := tracker.Ingest(candidate("late", "archive", base.Add(5*time.Minute), 10, 20, 1, 40))
	if err != nil || late.Status != ContainedStatus {
		t.Fatalf("late historical evidence must not reopen containment: %+v, %v", late, err)
	}
	changed, err = tracker.Advance(base.Add(3 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Status != ClosedStatus {
		t.Fatalf("expected closure, got %+v", changed)
	}
	reopened, err := tracker.Ingest(candidate("c", "ground", base.Add(4*time.Hour), 10.005, 20, 50, 95))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != first.ID || reopened.Status != ActiveStatus || !reopened.ClosedAt.IsZero() {
		t.Fatalf("expected same incident to reopen: %+v", reopened)
	}
	if got := reopened.Transitions[len(reopened.Transitions)-1]; got.From != ClosedStatus || got.To != ActiveStatus {
		t.Fatalf("missing deterministic reopen transition: %+v", got)
	}
}

func TestTrackerAssociationOrderingAndDatelineBounds(t *testing.T) {
	base := time.Date(2025, 8, 2, 0, 0, 0, 0, time.UTC)
	config := testConfig()
	config.ActivationEvidence = 3
	config.ActivationSources = 1
	config.AssociationDistance = 50_000
	tracker, err := NewTracker(config)
	if err != nil {
		t.Fatal(err)
	}
	west, err := tracker.Ingest(candidate("west", "s", base, 0, 179.9, 5, 50))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := tracker.Ingest(candidate("east", "s", base.Add(time.Minute), 0, -179.9, 7, 70))
	if err != nil {
		t.Fatal(err)
	}
	if joined.ID != west.ID || !joined.Boundary.CrossesDateline() {
		t.Fatalf("expected dateline-spanning association: %+v", joined)
	}
	if joined.Centroid.Lon < 179 && joined.Centroid.Lon > -179 {
		t.Fatalf("centroid should remain near dateline: %+v", joined.Centroid)
	}
	_, err = tracker.Ingest(candidate("far", "s", base.Add(2*time.Minute), 20, 20, 1, 10))
	if err != nil {
		t.Fatal(err)
	}
	list := tracker.List()
	if len(list) != 2 || list[0].FirstSeen.After(list[1].FirstSeen) {
		t.Fatalf("list is not deterministic: %+v", list)
	}
	copyList := tracker.List()
	copyList[0].Evidence[0].Source = "mutated"
	original, _ := tracker.Get(copyList[0].ID)
	if original.Evidence[0].Source == "mutated" {
		t.Fatal("returned snapshots alias tracker state")
	}
}

func TestTrackerDuplicateAndValidationErrors(t *testing.T) {
	base := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	tracker, err := NewTracker(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	value := candidate("same", "source", base, 1, 2, 3, 4)
	first, err := tracker.Ingest(value)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := tracker.Ingest(value)
	if err != nil || duplicate.ID != first.ID || len(duplicate.Evidence) != 1 {
		t.Fatalf("duplicate should be idempotent: %+v, %v", duplicate, err)
	}
	value.FRP = 99
	if _, err := tracker.Ingest(value); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected conflicting duplicate error, got %v", err)
	}
	bad := value
	bad.EvidenceID = ""
	if _, err := tracker.Ingest(bad); err == nil || !strings.Contains(err.Error(), "evidence ID") {
		t.Fatalf("expected clear validation error, got %v", err)
	}
}
