package alert_test

import (
	"reflect"
	"testing"
	"time"

	"firescope/alert"
	"firescope/geo"
	"firescope/incident"
)

func publicRules() []alert.Rule {
	return []alert.Rule{
		{
			ID: "rule-a", MinFRP: 1, MinConfidence: 1,
			Statuses: []incident.Status{incident.ActiveStatus},
			Channels: []string{"email", "sms"}, Severity: alert.Warning,
			SuppressionFor: time.Hour,
		},
		{
			ID: "rule-b", MinFRP: 1, MinConfidence: 1,
			Statuses: []incident.Status{incident.ActiveStatus},
			Channels: []string{"pager", "webhook"}, Severity: alert.Critical,
			SuppressionFor: time.Hour,
		},
	}
}

func publicIncident(id string, at time.Time) incident.Incident {
	return incident.Incident{
		ID: id, Status: incident.ActiveStatus,
		FirstSeen: at, LastSeen: at, UpdatedAt: at,
		Centroid: geo.Point{Lat: 1, Lon: 2},
		Boundary: geo.BoundingBox{South: 1, West: 2, North: 1, East: 2},
		FRPPeak:  20, Confidence: 80,
		Evidence: []incident.Candidate{{EvidenceID: id + "-evidence"}},
	}
}

func newPublicRouter(t *testing.T) *alert.Router {
	t.Helper()
	router, err := alert.NewRouter(publicRules())
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestClearIncidentFailureIsAtomic(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	item := publicIncident("incident-target", base)
	seed := newPublicRouter(t)
	if got, err := seed.Route(item, base); err != nil || len(got) != 4 {
		t.Fatalf("seed route: len=%d err=%v", len(got), err)
	}
	snapshot := seed.Snapshot()
	// Make exactly one target delivery newer than the requested clear time. The
	// operation must reject the whole clear without changing any other delivery.
	snapshot.Envelopes[0].EmittedAt = base.Add(2 * time.Hour)

	// Fresh maps vary traversal order. Every attempt must preserve the complete
	// snapshot, so this catches partial writes without assuming an iteration order.
	for attempt := 0; attempt < 64; attempt++ {
		router := newPublicRouter(t)
		if err := router.Restore(snapshot); err != nil {
			t.Fatal(err)
		}
		before := router.Snapshot()
		if _, err := router.ClearIncident(item.ID, base.Add(time.Hour), "resolved"); err == nil {
			t.Fatal("clear unexpectedly accepted a time preceding a delivery")
		}
		if after := router.Snapshot(); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed clear partially changed router state on attempt %d\nbefore: %+v\nafter:  %+v", attempt, before, after)
		}
	}
}

func TestClearIncidentResetsOnlyItsSuppressionRoutes(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	router := newPublicRouter(t)
	target := publicIncident("incident-target", base)
	// Deliberately share the target ID prefix so a string-prefix route deletion
	// would corrupt this unrelated incident's suppression state.
	other := publicIncident("incident-target-child", base)

	for _, item := range []incident.Incident{target, other} {
		got, err := router.Route(item, base)
		if err != nil || len(got) != 4 {
			t.Fatalf("initial multi-rule/channel route for %q: len=%d err=%v", item.ID, len(got), err)
		}
	}
	cleared, err := router.ClearIncident(target.ID, base.Add(time.Minute), "resolved")
	if err != nil || len(cleared) != 4 {
		t.Fatalf("clear target: len=%d err=%v", len(cleared), err)
	}

	reopened, err := router.Route(target, base.Add(2*time.Minute))
	if err != nil || len(reopened) != 4 {
		t.Fatalf("cleared incident must send immediately when reopened: len=%d err=%v", len(reopened), err)
	}
	stillSuppressed, err := router.Route(other, base.Add(2*time.Minute))
	if err != nil || len(stillSuppressed) != 0 {
		t.Fatalf("other incident suppression route changed: len=%d err=%v", len(stillSuppressed), err)
	}
}
