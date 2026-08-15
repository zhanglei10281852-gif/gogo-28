package alert

import (
	"reflect"
	"testing"
	"time"

	"firescope/geo"
	"firescope/incident"
)

func activeIncident(t *testing.T, at time.Time) (*incident.Tracker, incident.Incident) {
	t.Helper()
	config := incident.DefaultConfig()
	config.ActivationEvidence = 1
	config.ActivationSources = 1
	tracker, err := incident.NewTracker(config)
	if err != nil {
		t.Fatal(err)
	}
	item, err := tracker.Ingest(incident.Candidate{
		EvidenceID: "one", Source: "satellite", ObservedAt: at,
		Location: geo.Point{Lat: 5, Lon: 6}, FRP: 20, Confidence: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tracker, item
}

func testRule(t *testing.T) Rule {
	t.Helper()
	region, err := geo.NewPolygon([]geo.Point{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 10}, {Lat: 10, Lon: 10}, {Lat: 10, Lon: 0}})
	if err != nil {
		t.Fatal(err)
	}
	return Rule{
		ID: "regional-fire", Region: &region, MinFRP: 10, MinConfidence: 50,
		Statuses: []incident.Status{incident.ActiveStatus},
		Channels: []string{"sms", "email"}, Severity: Warning,
		Escalations:    []Escalation{{MinFRP: 75, MinConfidence: 70, Severity: Critical}},
		SuppressionFor: 30 * time.Minute,
	}
}

func TestRouteSuppressionEscalationAndLifecycle(t *testing.T) {
	base := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)
	tracker, item := activeIncident(t, base)
	router, err := NewRouter([]Rule{testRule(t)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := router.Route(item, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Channel != "email" || first[1].Channel != "sms" || first[0].Severity != Warning {
		t.Fatalf("unexpected fanout: %+v", first)
	}
	suppressed, err := router.Route(item, base.Add(time.Minute))
	if err != nil || len(suppressed) != 0 {
		t.Fatalf("expected suppression, got %+v, %v", suppressed, err)
	}
	item, err = tracker.Ingest(incident.Candidate{
		EvidenceID: "two", Source: "ground", ObservedAt: base.Add(2 * time.Minute),
		Location: geo.Point{Lat: 5.01, Lon: 6}, FRP: 100, Confidence: 95,
	})
	if err != nil {
		t.Fatal(err)
	}
	escalated, err := router.Route(item, base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(escalated) != 2 || escalated[0].Severity != Critical {
		t.Fatalf("severity upgrade should bypass suppression: %+v", escalated)
	}
	acknowledged, err := router.Acknowledge(escalated[0].ID, base.Add(3*time.Minute), "operator-7")
	if err != nil || acknowledged.AcknowledgedBy != "operator-7" {
		t.Fatalf("acknowledgment failed: %+v, %v", acknowledged, err)
	}
	cleared, err := router.ClearIncident(item.ID, base.Add(4*time.Minute), "incident resolved")
	if err != nil || len(cleared) != 4 {
		t.Fatalf("clear failed: %d, %v", len(cleared), err)
	}
	for _, envelope := range cleared {
		if envelope.Active() || envelope.ClearReason != "incident resolved" {
			t.Fatalf("bad cleared envelope: %+v", envelope)
		}
	}
}

func TestSnapshotRestoreAndDeterministicOrdering(t *testing.T) {
	base := time.Date(2025, 11, 3, 4, 5, 6, 0, time.UTC)
	_, item := activeIncident(t, base)
	rule := testRule(t)
	router, err := NewRouter([]Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Route(item, base); err != nil {
		t.Fatal(err)
	}
	snapshot := router.Snapshot()
	if len(snapshot.Routes) != 2 || snapshot.Routes[0].Key > snapshot.Routes[1].Key {
		t.Fatalf("snapshot routes not sorted: %+v", snapshot.Routes)
	}
	restored, err := NewRouter([]Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Snapshot(), snapshot) {
		t.Fatalf("snapshot round trip changed state\nwant: %+v\ngot:  %+v", snapshot, restored.Snapshot())
	}
	suppressed, err := restored.Route(item, base.Add(10*time.Minute))
	if err != nil || len(suppressed) != 0 {
		t.Fatalf("restored suppression not applied: %+v, %v", suppressed, err)
	}
	afterWindow, err := restored.Route(item, base.Add(31*time.Minute))
	if err != nil || len(afterWindow) != 2 {
		t.Fatalf("delivery after suppression failed: %+v, %v", afterWindow, err)
	}
	if afterWindow[0].ID == snapshot.Envelopes[0].ID {
		t.Fatal("new delivery reused envelope ID")
	}
}

func TestRuleFiltersAndErrors(t *testing.T) {
	base := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	_, item := activeIncident(t, base)
	rule := testRule(t)
	rule.MinFRP = 1000
	router, err := NewRouter([]Rule{rule})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Route(item, base)
	if err != nil || len(result) != 0 {
		t.Fatalf("threshold filter failed: %+v, %v", result, err)
	}
	bad := rule
	bad.ID = ""
	if _, err := NewRouter([]Rule{bad}); err == nil {
		t.Fatal("expected invalid rule error")
	}
	if _, err := router.Acknowledge("missing", base, "operator"); err == nil {
		t.Fatal("expected missing alert error")
	}
	if err := router.Restore(Snapshot{Routes: []RouteSnapshot{{Key: "bad"}}}); err == nil {
		t.Fatal("expected invalid snapshot error")
	}
}
