package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Report {
	t.Helper()
	zone := time.FixedZone("west", -7*3600)
	start := time.Date(2025, 7, 8, 9, 0, 0, 0, zone)
	input := Input{
		GeneratedAt: start.Add(5 * time.Hour),
		Window:      Window{Start: start, End: start.Add(4 * time.Hour)},
		Incidents: []Incident{
			{ID: "b", Name: "Bravo", Severity: SeverityLow, Status: StatusMonitoring, StartedAt: start.Add(time.Hour), UpdatedAt: start.Add(2 * time.Hour), Sources: []string{"SAT-B", "SAT-A"}},
			{ID: "a", Name: "Alpha, \"ridge\"\nline", Severity: SeverityCritical, Status: StatusActive, StartedAt: start, UpdatedAt: start.Add(time.Hour), Sources: []string{"SAT-A"}},
		},
		Observations: []Observation{
			{ID: "o2", IncidentID: "b", Source: "SAT-B", AcquiredAt: start.Add(65 * time.Minute), Latitude: 2, Longitude: 3, FRP: 7.5, Confidence: 80},
			{ID: "o1", IncidentID: "a", Source: "SAT-A", AcquiredAt: start.Add(5 * time.Minute), Latitude: 1, Longitude: 2, FRP: 12.5, Confidence: 90},
		},
	}
	report, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestBuildNormalizesSortsAndSummarizes(t *testing.T) {
	report := fixture(t)
	if report.GeneratedAt.Location() != time.UTC || report.Window.Start.Location() != time.UTC {
		t.Fatal("times were not normalized to UTC")
	}
	if report.Incidents[0].ID != "a" || report.Observations[0].ID != "o1" {
		t.Fatal("records were not deterministically sorted")
	}
	if report.Summary.IncidentCount != 2 || report.Summary.ObservationCount != 2 || report.Summary.TotalFRP != 20 || report.Summary.MaximumFRP != 12.5 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	wantSeverity := []Count{{Name: "critical", Count: 1}, {Name: "low", Count: 1}}
	if !reflect.DeepEqual(report.Summary.BySeverity, wantSeverity) {
		t.Fatalf("severity = %#v", report.Summary.BySeverity)
	}
	wantSources := []SourceCount{{Source: "SAT-A", Observations: 1, Incidents: 2}, {Source: "SAT-B", Observations: 1, Incidents: 1}}
	if !reflect.DeepEqual(report.Summary.BySource, wantSources) {
		t.Fatalf("sources = %#v", report.Summary.BySource)
	}
	if len(report.Summary.ByHour) != 2 || report.Incidents[0].ObservationCount != 1 {
		t.Fatalf("hour/link aggregates missing: %+v", report.Summary.ByHour)
	}
}
func TestJSONAndTextAreDeterministic(t *testing.T) {
	report := fixture(t)
	first, err := JSON(report)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := JSON(report)
	if !bytes.Equal(first, second) || !json.Valid(first) {
		t.Fatal("JSON is invalid or nondeterministic")
	}
	if bytes.Contains(first, []byte(`\u003c`)) {
		t.Fatal("HTML escaping unexpectedly enabled")
	}
	text, err := Text(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"FireScope Report", "Generated: 2025-07-08T21:00:00Z", "critical: 1", "SAT-A: 1 observations, 2 incidents"} {
		if !strings.Contains(string(text), expected) {
			t.Errorf("text missing %q:\n%s", expected, text)
		}
	}
}

func TestCSVEscapingAndHeaders(t *testing.T) {
	report := fixture(t)
	data, err := IncidentCSV(report)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1][1] != "Alpha, \"ridge\"\nline" {
		t.Fatalf("CSV did not round trip escaped name: %#v", rows)
	}
	if rows[2][7] != "SAT-A;SAT-B" {
		t.Fatalf("sources not sorted/joined: %#v", rows[2])
	}
	observationData, err := ObservationCSV(report)
	if err != nil {
		t.Fatal(err)
	}
	observationRows, err := csv.NewReader(bytes.NewReader(observationData)).ReadAll()
	if err != nil || len(observationRows) != 3 || observationRows[1][0] != "o1" {
		t.Fatalf("observation CSV = %#v, %v", observationRows, err)
	}
}

func TestEmptyReportHasUsefulOutput(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	report, err := Build(Input{Window: Window{Start: start, End: start.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.GeneratedAt.Equal(time.Unix(0, 0)) {
		t.Fatalf("zero generated time fallback = %s", report.GeneratedAt)
	}
	observations, _ := ObservationCSV(report)
	incidents, _ := IncidentCSV(report)
	if strings.Count(string(observations), "\n") != 1 || strings.Count(string(incidents), "\n") != 1 {
		t.Fatalf("empty CSV output should contain exactly headers: %q / %q", observations, incidents)
	}
	text, _ := Text(report)
	if !strings.Contains(string(text), "Incidents: 0") || !strings.Contains(string(text), "  (none)") {
		t.Fatalf("empty text report is not useful:\n%s", text)
	}
	jsonData, _ := JSON(report)
	if !bytes.Contains(jsonData, []byte(`"incidents": []`)) || !bytes.Contains(jsonData, []byte(`"observations": []`)) {
		t.Fatalf("empty JSON arrays must not be null:\n%s", jsonData)
	}
}

func TestBuildRejectsInvalidLinksAndWindow(t *testing.T) {
	start := time.Now()
	_, err := Build(Input{
		Window:       Window{Start: start, End: start.Add(time.Hour)},
		Observations: []Observation{{ID: "o", IncidentID: "missing", Source: "s", AcquiredAt: start, FRP: 1, Confidence: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown incident") {
		t.Fatalf("expected unknown incident error, got %v", err)
	}
	_, err = Build(Input{Window: Window{Start: start, End: start.Add(-time.Hour)}})
	if err == nil {
		t.Fatal("expected reversed window error")
	}
}

func JSON(report Report) ([]byte, error) {
	var out bytes.Buffer
	err := WriteJSON(&out, report)
	return out.Bytes(), err
}

func Text(report Report) ([]byte, error) {
	var out bytes.Buffer
	err := WriteText(&out, report)
	return out.Bytes(), err
}

func ObservationCSV(report Report) ([]byte, error) {
	var out bytes.Buffer
	err := WriteObservationCSV(&out, report)
	return out.Bytes(), err
}

func IncidentCSV(report Report) ([]byte, error) {
	var out bytes.Buffer
	err := WriteIncidentCSV(&out, report)
	return out.Bytes(), err
}
