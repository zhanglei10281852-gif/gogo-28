package firescope_test

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"firescope/alert"
	"firescope/fusion"
	"firescope/incident"
	"firescope/ingest"
	"firescope/report"
	"firescope/store"
)

const integrationJSON = `[
 {"id":"a","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:00:00Z","latitude":34,"longitude":-118,"brightness":330,"brightness_t31":290,"frp":10,"confidence":70,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"a2","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:01:00Z","latitude":34.0001,"longitude":-118,"brightness":335,"brightness_t31":291,"frp":20,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"b","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:20:00Z","latitude":34.0002,"longitude":-118,"brightness":332,"brightness_t31":290,"frp":15,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"c","satellite":"AQUA","instrument":"MODIS","acquired_at":"2026-07-07T12:20:30Z","latitude":34.0003,"longitude":-118,"brightness":334,"brightness_t31":290,"frp":18,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]}
]`

func TestCrossPackageProcessingJSONCSVAndReport(t *testing.T) {
	loaded, err := ingest.Ingest(strings.NewReader(integrationJSON), ingest.Options{Format: ingest.FormatAuto})
	if err != nil || loaded.Stats.Accepted != 4 {
		t.Fatalf("JSON ingest: stats=%+v err=%v", loaded.Stats, err)
	}
	csvInput := "satellite,instrument,acquired_at,latitude,longitude,brightness,brightness_t31,frp,confidence,day_night,scan,track,quality_flags,id\n" +
		"NOAA-20,VIIRS,2026-07-07T12:00:00Z,34,-118,330,290,10,70,D,1,1,,csv-one\n"
	csvResult, err := ingest.Ingest(strings.NewReader(csvInput), ingest.Options{Format: ingest.FormatCSV})
	if err != nil || csvResult.Stats.Accepted != 1 || csvResult.Observations[0].ID != "csv-one" {
		t.Fatalf("CSV ingest: result=%+v err=%v", csvResult, err)
	}

	fused, err := fusion.Process(loaded.Observations, fusion.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(fused.DuplicateGroups) != 1 || fused.DuplicateGroups[0].Winner.ID != "a2" || len(fused.Observations) != 2 {
		t.Fatalf("dedup/fusion result=%+v", fused)
	}
	incidentConfig := incident.DefaultConfig()
	incidentConfig.AssociationWindow = time.Hour
	incidentConfig.ActivationEvidence = 2
	incidentConfig.ActivationSources = 1
	tracker, err := incident.NewTracker(incidentConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range fused.Observations {
		if _, err := tracker.Ingest(incident.Candidate{EvidenceID: value.ID, Source: strings.Join(value.Sources, "+"), ObservedAt: value.ObservedAt, Location: value.Location, FRP: value.FRP, Confidence: value.Confidence}); err != nil {
			t.Fatal(err)
		}
	}
	items := tracker.List()
	if len(items) != 1 || items[0].Status != incident.ActiveStatus {
		t.Fatalf("incident did not activate: %+v", items)
	}
	router, err := alert.NewRouter([]alert.Rule{{ID: "ops", MinFRP: 5, MinConfidence: 60, Statuses: []incident.Status{incident.ActiveStatus}, Channels: []string{"console"}, Severity: alert.Warning, SuppressionFor: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 7, 12, 20, 30, 0, time.UTC)
	envelopes, err := router.Route(items[0], now)
	if err != nil || len(envelopes) != 1 {
		t.Fatalf("alerts=%+v err=%v", envelopes, err)
	}

	reportObservations := make([]report.Observation, 0, len(fused.Observations))
	for _, value := range fused.Observations {
		reportObservations = append(reportObservations, report.Observation{ID: value.ID, IncidentID: items[0].ID, Source: strings.Join(value.Sources, "+"), AcquiredAt: value.ObservedAt, Latitude: value.Location.Lat, Longitude: value.Location.Lon, FRP: value.FRP, Confidence: value.Confidence})
	}
	built, err := report.Build(report.Input{
		GeneratedAt: now,
		Window:      report.Window{Start: fused.Observations[0].ObservedAt, End: now},
		Incidents: []report.Incident{{
			ID: items[0].ID, Name: "Integrated incident", Severity: report.SeverityHigh,
			Status: report.StatusActive, StartedAt: items[0].FirstSeen, UpdatedAt: items[0].UpdatedAt,
			Sources: items[0].Sources,
		}},
		Observations: reportObservations,
	})
	if err != nil {
		t.Fatal(err)
	}
	var jsonOne, jsonTwo, text bytes.Buffer
	if err := report.WriteJSON(&jsonOne, built); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteJSON(&jsonTwo, built); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteText(&text, built); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonOne.Bytes(), jsonTwo.Bytes()) || !strings.Contains(text.String(), "Incidents: 1") || built.Summary.ObservationCount != 2 {
		t.Fatalf("nondeterministic or incomplete report:\n%s", text.String())
	}
}

func TestSnapshotRoundTripFromIngestedOrbitBatch(t *testing.T) {
	batchJSON := `{"orbit_id":"orbit-1","satellite":"NOAA-20","window_start":"","window_end":"","observations":[{"id":"one","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:00:00Z","latitude":34,"longitude":-118,"brightness":330,"brightness_t31":290,"frp":10,"confidence":70,"day_night":"D","scan":1,"track":1,"quality_flags":[]}]}`
	loaded, err := ingest.Ingest(strings.NewReader(batchJSON), ingest.Options{})
	if err != nil || len(loaded.Batches) != 1 {
		t.Fatalf("batch ingest=%+v err=%v", loaded, err)
	}
	created := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	snapshot, err := store.NewSnapshot("roundtrip", created, loaded.Batches, []store.Tag{{Key: "source", Value: "integration-test"}})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.NewFileStore(filepath.Join(t.TempDir(), "snapshots"), store.FileOptions{CreateDirectory: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("roundtrip", snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := repository.Load("roundtrip")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, restored) || restored.Metadata.ObservationCount != 1 {
		t.Fatalf("snapshot changed after round trip:\nwant=%+v\ngot=%+v", snapshot, restored)
	}
}
