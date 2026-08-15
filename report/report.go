// Package report builds deterministic summaries and text, JSON, and CSV output.
package report

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Severity is deliberately independent from incident package types.
type Severity string

const (
	SeverityInformational Severity = "informational"
	SeverityLow           Severity = "low"
	SeverityModerate      Severity = "moderate"
	SeverityHigh          Severity = "high"
	SeverityCritical      Severity = "critical"
)

// Status is the reporting lifecycle of an incident.
type Status string

const (
	StatusDetected   Status = "detected"
	StatusActive     Status = "active"
	StatusMonitoring Status = "monitoring"
	StatusContained  Status = "contained"
	StatusClosed     Status = "closed"
)

// Window is an inclusive UTC reporting interval.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Observation is a stable reporting DTO, not a persistence or model type.
type Observation struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id,omitempty"`
	Source     string    `json:"source"`
	AcquiredAt time.Time `json:"acquired_at"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	FRP        float64   `json:"frp_mw"`
	Confidence float64   `json:"confidence"`
}

// Incident is the report's incident projection.
type Incident struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Severity         Severity   `json:"severity"`
	Status           Status     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ClosedAt         *time.Time `json:"closed_at,omitempty"`
	Sources          []string   `json:"sources"`
	ObservationCount int        `json:"observation_count"`
}

// Count is a sorted categorical aggregate.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SourceCount reports both observations and incidents attributed to a source.
type SourceCount struct {
	Source       string `json:"source"`
	Observations int    `json:"observations"`
	Incidents    int    `json:"incidents"`
}

// TimeBucket is an hourly UTC observation aggregate.
type TimeBucket struct {
	Start        time.Time `json:"start"`
	Observations int       `json:"observations"`
	FRP          float64   `json:"frp_mw"`
}

// Summary contains report-wide deterministic aggregates.
type Summary struct {
	IncidentCount    int           `json:"incident_count"`
	ObservationCount int           `json:"observation_count"`
	TotalFRP         float64       `json:"total_frp_mw"`
	MaximumFRP       float64       `json:"maximum_frp_mw"`
	BySeverity       []Count       `json:"by_severity"`
	ByStatus         []Count       `json:"by_status"`
	BySource         []SourceCount `json:"by_source"`
	ByHour           []TimeBucket  `json:"by_hour"`
}

// Input contains unnormalized data accepted by Build.
type Input struct {
	GeneratedAt  time.Time
	Window       Window
	Incidents    []Incident
	Observations []Observation
}

// Report is a fully normalized, deterministic output document.
type Report struct {
	GeneratedAt  time.Time     `json:"generated_at"`
	Window       Window        `json:"window"`
	Summary      Summary       `json:"summary"`
	Incidents    []Incident    `json:"incidents"`
	Observations []Observation `json:"observations"`
}

// Build validates, normalizes, sorts, and aggregates a report.
func Build(input Input) (Report, error) {
	generated := input.GeneratedAt
	if generated.IsZero() {
		generated = time.Unix(0, 0)
	}
	generated = generated.UTC()
	window, err := normalizeWindow(input.Window)
	if err != nil {
		return Report{}, err
	}
	incidents := append([]Incident(nil), input.Incidents...)
	observations := append([]Observation(nil), input.Observations...)
	if incidents == nil {
		incidents = []Incident{}
	}
	if observations == nil {
		observations = []Observation{}
	}
	incidentIDs := make(map[string]struct{}, len(incidents))
	for i := range incidents {
		if err := normalizeIncident(&incidents[i], window); err != nil {
			return Report{}, fmt.Errorf("incident %d: %w", i, err)
		}
		if _, exists := incidentIDs[incidents[i].ID]; exists {
			return Report{}, fmt.Errorf("duplicate incident ID %q", incidents[i].ID)
		}
		incidentIDs[incidents[i].ID] = struct{}{}
	}
	observationIDs := make(map[string]struct{}, len(observations))
	linked := make(map[string]int)
	for i := range observations {
		if err := normalizeObservation(&observations[i], window); err != nil {
			return Report{}, fmt.Errorf("observation %d: %w", i, err)
		}
		if _, exists := observationIDs[observations[i].ID]; exists {
			return Report{}, fmt.Errorf("duplicate observation ID %q", observations[i].ID)
		}
		observationIDs[observations[i].ID] = struct{}{}
		if observations[i].IncidentID != "" {
			if _, exists := incidentIDs[observations[i].IncidentID]; !exists {
				return Report{}, fmt.Errorf("observation %q references unknown incident %q", observations[i].ID, observations[i].IncidentID)
			}
			linked[observations[i].IncidentID]++
		}
	}
	for i := range incidents {
		if incidents[i].ObservationCount == 0 {
			incidents[i].ObservationCount = linked[incidents[i].ID]
		} else if incidents[i].ObservationCount != linked[incidents[i].ID] {
			return Report{}, fmt.Errorf("incident %q observation count does not match linked observations", incidents[i].ID)
		}
	}
	sortIncidents(incidents)
	sortObservations(observations)
	report := Report{GeneratedAt: generated, Window: window, Incidents: incidents, Observations: observations}
	report.Summary = summarize(report)
	return report, nil
}

func normalizeWindow(window Window) (Window, error) {
	if window.Start.IsZero() || window.End.IsZero() {
		return Window{}, errors.New("report window start and end are required")
	}
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if window.End.Before(window.Start) {
		return Window{}, errors.New("report window end precedes start")
	}
	return window, nil
}

func normalizeIncident(incident *Incident, window Window) error {
	incident.ID = strings.TrimSpace(incident.ID)
	incident.Name = strings.TrimSpace(incident.Name)
	if incident.ID == "" || incident.Name == "" {
		return errors.New("ID and name are required")
	}
	if severityRank(incident.Severity) < 0 {
		return fmt.Errorf("invalid severity %q", incident.Severity)
	}
	if statusRank(incident.Status) < 0 {
		return fmt.Errorf("invalid status %q", incident.Status)
	}
	if incident.StartedAt.IsZero() || incident.UpdatedAt.IsZero() {
		return errors.New("start and update times are required")
	}
	incident.StartedAt = incident.StartedAt.UTC()
	incident.UpdatedAt = incident.UpdatedAt.UTC()
	if incident.UpdatedAt.Before(incident.StartedAt) {
		return errors.New("update time precedes start")
	}
	if incident.StartedAt.After(window.End) || incident.UpdatedAt.Before(window.Start) {
		return errors.New("incident does not overlap report window")
	}
	if incident.ClosedAt != nil {
		closed := incident.ClosedAt.UTC()
		incident.ClosedAt = &closed
		if closed.Before(incident.StartedAt) {
			return errors.New("close time precedes start")
		}
		if incident.Status != StatusClosed {
			return errors.New("close time requires closed status")
		}
	} else if incident.Status == StatusClosed {
		return errors.New("closed status requires close time")
	}
	if incident.ObservationCount < 0 {
		return errors.New("observation count cannot be negative")
	}
	incident.Sources = normalizeStrings(incident.Sources)
	if incident.Sources == nil {
		incident.Sources = []string{}
	}
	return nil
}

func normalizeObservation(observation *Observation, window Window) error {
	observation.ID = strings.TrimSpace(observation.ID)
	observation.IncidentID = strings.TrimSpace(observation.IncidentID)
	observation.Source = strings.TrimSpace(observation.Source)
	if observation.ID == "" || observation.Source == "" || observation.AcquiredAt.IsZero() {
		return errors.New("ID, source, and acquisition time are required")
	}
	observation.AcquiredAt = observation.AcquiredAt.UTC()
	if observation.AcquiredAt.Before(window.Start) || observation.AcquiredAt.After(window.End) {
		return errors.New("acquisition time is outside report window")
	}
	values := []float64{observation.Latitude, observation.Longitude, observation.FRP, observation.Confidence}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("numeric fields must be finite")
		}
	}
	if observation.Latitude < -90 || observation.Latitude > 90 || observation.Longitude < -180 || observation.Longitude > 180 {
		return errors.New("coordinates are out of range")
	}
	if observation.FRP < 0 || observation.Confidence < 0 || observation.Confidence > 100 {
		return errors.New("FRP or confidence is out of range")
	}
	return nil
}

func normalizeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
func severityRank(value Severity) int {
	order := []Severity{SeverityCritical, SeverityHigh, SeverityModerate, SeverityLow, SeverityInformational}
	for i, candidate := range order {
		if value == candidate {
			return i
		}
	}
	return -1
}

func statusRank(value Status) int {
	order := []Status{StatusDetected, StatusActive, StatusMonitoring, StatusContained, StatusClosed}
	for i, candidate := range order {
		if value == candidate {
			return i
		}
	}
	return -1
}

func sortIncidents(incidents []Incident) {
	sort.SliceStable(incidents, func(i, j int) bool {
		if incidents[i].StartedAt.Equal(incidents[j].StartedAt) {
			if severityRank(incidents[i].Severity) == severityRank(incidents[j].Severity) {
				return incidents[i].ID < incidents[j].ID
			}
			return severityRank(incidents[i].Severity) < severityRank(incidents[j].Severity)
		}
		return incidents[i].StartedAt.Before(incidents[j].StartedAt)
	})
}

func sortObservations(observations []Observation) {
	sort.SliceStable(observations, func(i, j int) bool {
		if observations[i].AcquiredAt.Equal(observations[j].AcquiredAt) {
			return observations[i].ID < observations[j].ID
		}
		return observations[i].AcquiredAt.Before(observations[j].AcquiredAt)
	})
}

func summarize(report Report) Summary {
	severity := make(map[string]int)
	status := make(map[string]int)
	type mutableSource struct{ observations, incidents int }
	sources := make(map[string]*mutableSource)
	hours := make(map[time.Time]*TimeBucket)
	for _, incident := range report.Incidents {
		severity[string(incident.Severity)]++
		status[string(incident.Status)]++
		for _, source := range incident.Sources {
			entry := sources[source]
			if entry == nil {
				entry = &mutableSource{}
				sources[source] = entry
			}
			entry.incidents++
		}
	}
	summary := Summary{IncidentCount: len(report.Incidents), ObservationCount: len(report.Observations)}
	for _, observation := range report.Observations {
		summary.TotalFRP += observation.FRP
		if observation.FRP > summary.MaximumFRP {
			summary.MaximumFRP = observation.FRP
		}
		entry := sources[observation.Source]
		if entry == nil {
			entry = &mutableSource{}
			sources[observation.Source] = entry
		}
		entry.observations++
		hour := observation.AcquiredAt.Truncate(time.Hour)
		bucket := hours[hour]
		if bucket == nil {
			bucket = &TimeBucket{Start: hour}
			hours[hour] = bucket
		}
		bucket.Observations++
		bucket.FRP += observation.FRP
	}
	summary.BySeverity = orderedCounts(severity, []string{"critical", "high", "moderate", "low", "informational"})
	summary.ByStatus = orderedCounts(status, []string{"detected", "active", "monitoring", "contained", "closed"})
	sourceNames := make([]string, 0, len(sources))
	for name := range sources {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)
	summary.BySource = make([]SourceCount, 0, len(sourceNames))
	for _, name := range sourceNames {
		entry := sources[name]
		summary.BySource = append(summary.BySource, SourceCount{Source: name, Observations: entry.observations, Incidents: entry.incidents})
	}
	hourNames := make([]time.Time, 0, len(hours))
	for hour := range hours {
		hourNames = append(hourNames, hour)
	}
	sort.Slice(hourNames, func(i, j int) bool { return hourNames[i].Before(hourNames[j]) })
	summary.ByHour = make([]TimeBucket, 0, len(hourNames))
	for _, hour := range hourNames {
		summary.ByHour = append(summary.ByHour, *hours[hour])
	}
	return summary
}

func orderedCounts(values map[string]int, order []string) []Count {
	result := make([]Count, 0, len(values))
	for _, name := range order {
		if count := values[name]; count != 0 {
			result = append(result, Count{Name: name, Count: count})
		}
	}
	return result
}

// WriteJSON emits indented deterministic JSON followed by one newline.
func WriteJSON(writer io.Writer, report Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	return nil
}

// WriteText emits a compact human-readable report with fixed section ordering.
func WriteText(writer io.Writer, report Report) error {
	var text strings.Builder
	fmt.Fprintf(&text, "FireScope Report\nGenerated: %s\nWindow: %s — %s\n\n", formatTime(report.GeneratedAt), formatTime(report.Window.Start), formatTime(report.Window.End))
	fmt.Fprintf(&text, "Summary\n  Incidents: %d\n  Observations: %d\n  Total FRP: %s MW\n  Maximum FRP: %s MW\n", report.Summary.IncidentCount, report.Summary.ObservationCount, number(report.Summary.TotalFRP), number(report.Summary.MaximumFRP))
	writeCounts(&text, "Severity", report.Summary.BySeverity)
	writeCounts(&text, "Status", report.Summary.ByStatus)
	text.WriteString("Sources\n")
	if len(report.Summary.BySource) == 0 {
		text.WriteString("  (none)\n")
	}
	for _, source := range report.Summary.BySource {
		fmt.Fprintf(&text, "  %s: %d observations, %d incidents\n", source.Source, source.Observations, source.Incidents)
	}
	text.WriteString("Incidents\n")
	if len(report.Incidents) == 0 {
		text.WriteString("  (none)\n")
	}
	for _, incident := range report.Incidents {
		fmt.Fprintf(&text, "  [%s] %s | %s | %s | %s | %d observations\n", incident.ID, incident.Name, incident.Severity, incident.Status, formatTime(incident.StartedAt), incident.ObservationCount)
	}
	_, err := io.WriteString(writer, text.String())
	return err
}

func writeCounts(writer *strings.Builder, title string, counts []Count) {
	fmt.Fprintf(writer, "%s\n", title)
	if len(counts) == 0 {
		writer.WriteString("  (none)\n")
	}
	for _, count := range counts {
		fmt.Fprintf(writer, "  %s: %d\n", count.Name, count.Count)
	}
}

var observationHeader = []string{"id", "incident_id", "source", "acquired_at", "latitude", "longitude", "frp_mw", "confidence"}
var incidentHeader = []string{"id", "name", "severity", "status", "started_at", "updated_at", "closed_at", "sources", "observation_count"}

// WriteObservationCSV always emits a header, even for an empty report.
func WriteObservationCSV(writer io.Writer, report Report) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(observationHeader); err != nil {
		return err
	}
	for _, observation := range report.Observations {
		record := []string{
			observation.ID, observation.IncidentID, observation.Source,
			formatTime(observation.AcquiredAt), number(observation.Latitude),
			number(observation.Longitude), number(observation.FRP), number(observation.Confidence),
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

// WriteIncidentCSV always emits a header and joins sorted sources with semicolons.
func WriteIncidentCSV(writer io.Writer, report Report) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(incidentHeader); err != nil {
		return err
	}
	for _, incident := range report.Incidents {
		closed := ""
		if incident.ClosedAt != nil {
			closed = formatTime(*incident.ClosedAt)
		}
		record := []string{
			incident.ID, incident.Name, string(incident.Severity), string(incident.Status),
			formatTime(incident.StartedAt), formatTime(incident.UpdatedAt), closed,
			strings.Join(incident.Sources, ";"), strconv.Itoa(incident.ObservationCount),
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func number(value float64) string { return strconv.FormatFloat(value+0, 'f', -1, 64) }
