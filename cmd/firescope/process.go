package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"firescope/alert"
	"firescope/fusion"
	"firescope/geo"
	"firescope/incident"
	"firescope/ingest"
	appconfig "firescope/internal/config"
	"firescope/model"
	"firescope/report"
)

type fusionSummary struct {
	Observations    int `json:"observations"`
	DuplicateGroups int `json:"duplicate_groups"`
}

type processResult struct {
	Ingest     ingest.Stats        `json:"ingest"`
	Rejections []ingest.Rejection  `json:"rejections"`
	Fusion     fusionSummary       `json:"fusion"`
	Incidents  []incident.Incident `json:"incidents"`
	Alerts     []alert.Envelope    `json:"alerts"`
	Report     report.Report       `json:"report"`
}

func runProcess(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		writeProcessHelp(stdout)
		return 0
	}
	set := newFlagSet("process")
	inputPath := set.String("input", "-", "input path or - for stdin")
	formatName := set.String("format", "auto", "input format: auto, json, csv")
	outputName := set.String("output", "text", "output format: text, json")
	configPath := set.String("config", "", "strict JSON configuration file")
	nowText := set.String("now", "", "RFC3339 processing time (default: latest observation)")
	maxRecords := set.Int("max-records", 0, "maximum input records; 0 is unlimited")
	if err := set.Parse(args); err != nil {
		return commandError(stderr, "process", err, true)
	}
	if set.NArg() != 0 {
		return commandError(stderr, "process", errors.New("unexpected positional arguments"), true)
	}
	format, err := parseFormat(*formatName)
	if err != nil {
		return commandError(stderr, "process", err, true)
	}
	output, err := parseOutput(*outputName)
	if err != nil {
		return commandError(stderr, "process", err, true)
	}
	now, err := parseNow(*nowText)
	if err != nil {
		return commandError(stderr, "process", err, true)
	}
	result, err := processInput(*inputPath, format, *maxRecords, *configPath, now, stdin)
	if err != nil {
		return commandError(stderr, "process", err, false)
	}
	if err := writeProcessOutput(stdout, output, result); err != nil {
		return commandError(stderr, "process", err, false)
	}
	return 0
}

func writeProcessHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: firescope process [flags]

Reads observations from JSON or CSV and runs ingest -> fusion -> incident ->
alert -> report entirely offline. Business output is written to stdout.

Flags:
  --input PATH       input file, or - for stdin (default -)
  --format FORMAT    auto, json, or csv (default auto)
  --output FORMAT    text or json (default text)
  --config PATH      strict JSON configuration file (optional)
  --now TIME         RFC3339 processing/report time (default latest observation)
  --max-records N    maximum records; 0 means unlimited
`)
}

func processInput(path string, format ingest.Format, maxRecords int, configPath string, now time.Time, stdin io.Reader) (processResult, error) {
	reader, closeInput, err := openInput(path, stdin)
	if err != nil {
		return processResult{}, err
	}
	defer closeInput()
	loaded, err := ingest.Ingest(reader, ingest.Options{Format: format, MaxRecords: maxRecords})
	if err != nil {
		return processResult{}, err
	}
	if len(loaded.Observations) == 0 {
		return processResult{}, fmt.Errorf("no valid observations (rejected %d)", loaded.Stats.Rejected)
	}
	cfg := appconfig.Defaults()
	if configPath != "" {
		cfg, err = appconfig.LoadFile(configPath)
		if err != nil {
			return processResult{}, err
		}
	}
	fused, err := runFusion(loaded.Observations, cfg)
	if err != nil {
		return processResult{}, err
	}
	return finishPipeline(loaded, fused, cfg, now)
}

func fusionConfig(cfg appconfig.Config, observations []model.Observation) fusion.Config {
	result := fusion.DefaultConfig()
	result.DedupDistanceMeters = cfg.Dedup.DistanceMeters
	result.DedupWindow = cfg.Dedup.TimeWindow.Value()
	result.FusionDistanceMeters = cfg.Incident.MergeDistanceMeters
	result.FusionWindow = cfg.Fusion.MaxAge.Value()
	result.HalfLife = cfg.Fusion.MaxAge.Value()
	result.SourceWeights = make(map[string]float64)
	for _, observation := range observations {
		weight, ok := cfg.Fusion.SourceWeights[observation.Instrument]
		if !ok {
			weight, ok = cfg.Fusion.SourceWeights[observation.Satellite]
		}
		if ok {
			result.SourceWeights[fusion.SourceKey(observation.Satellite, observation.Instrument)] = weight
		}
	}
	return result
}

func runFusion(observations []model.Observation, cfg appconfig.Config) (fusion.Result, error) {
	fcfg := fusionConfig(cfg, observations)
	if !cfg.Dedup.Enabled {
		fcfg.DedupDistanceMeters = math.SmallestNonzeroFloat64
		fcfg.DedupWindow = time.Nanosecond
	}
	if cfg.Fusion.Enabled {
		return fusion.Process(observations, fcfg)
	}
	engine, err := fusion.New(fcfg)
	if err != nil {
		return fusion.Result{}, err
	}
	values := observations
	var groups []fusion.DuplicateGroup
	if cfg.Dedup.Enabled {
		values, groups, err = engine.Deduplicate(observations)
		if err != nil {
			return fusion.Result{}, err
		}
	}
	result := fusion.Result{DuplicateGroups: groups}
	for _, observation := range values {
		result.Observations = append(result.Observations, projectObservation(observation))
	}
	return result, nil
}

func projectObservation(value model.Observation) fusion.FusedObservation {
	source := fusion.SourceKey(value.Satellite, value.Instrument)
	return fusion.FusedObservation{
		ID:         "fused-" + value.ID,
		ObservedAt: value.AcquiredAt,
		Location:   geo.Point{Lat: value.Latitude, Lon: value.Longitude},
		FRP:        value.FRP,
		Confidence: value.Confidence,
		Flags:      value.Flags,
		Sources:    []string{source},
		Evidence: []fusion.Evidence{{
			Observation: value, Source: source, Weight: 1, AgeFactor: 1, QualityFactor: 1,
		}},
	}
}

func incidentConfig(cfg appconfig.Config) incident.Config {
	result := incident.DefaultConfig()
	result.AssociationDistance = cfg.Incident.MergeDistanceMeters
	result.AssociationWindow = cfg.Fusion.MaxAge.Value()
	result.ActivationEvidence = cfg.Incident.MinimumObservations
	result.ActivationSources = 1
	result.CandidateSilence = cfg.Incident.QuietPeriod.Value()
	result.ContainmentSilence = cfg.Incident.QuietPeriod.Value()
	result.ClosureSilence = 2 * cfg.Incident.QuietPeriod.Value()
	result.MaximumDuration = cfg.Incident.MaximumDuration.Value()
	result.ReopenWindow = 2 * cfg.Incident.QuietPeriod.Value()
	result.ReopenDistance = cfg.Incident.MergeDistanceMeters
	return result
}

func alertRules(cfg appconfig.Config) []alert.Rule {
	if !cfg.Alert.Enabled {
		return nil
	}
	cooldown := cfg.Alert.Cooldown.Value()
	if cooldown <= 0 {
		cooldown = time.Nanosecond
	}
	return []alert.Rule{{
		ID: "configured", MinFRP: cfg.Alert.MinimumFRP,
		MinConfidence: cfg.Alert.MinimumConfidence,
		Statuses:      []incident.Status{incident.ActiveStatus},
		Channels:      append([]string(nil), cfg.Alert.Channels...),
		Severity:      alert.Warning, SuppressionFor: cooldown,
	}}
}

func finishPipeline(loaded ingest.Result, fused fusion.Result, cfg appconfig.Config, now time.Time) (processResult, error) {
	if len(fused.Observations) == 0 {
		return processResult{}, errors.New("fusion produced no observations")
	}
	latest := fused.Observations[0].ObservedAt
	for _, value := range fused.Observations[1:] {
		if value.ObservedAt.After(latest) {
			latest = value.ObservedAt
		}
	}
	if now.IsZero() {
		now = latest
	}
	if now.Before(latest) {
		return processResult{}, fmt.Errorf("--now %s precedes latest observation %s", now.Format(time.RFC3339Nano), latest.Format(time.RFC3339Nano))
	}
	tracker, err := incident.NewTracker(incidentConfig(cfg))
	if err != nil {
		return processResult{}, err
	}
	links := make(map[string]string, len(fused.Observations))
	for _, value := range fused.Observations {
		source := strings.Join(value.Sources, "+")
		if source == "" {
			source = "unknown"
		}
		item, err := tracker.Ingest(incident.Candidate{
			EvidenceID: value.ID, Source: source, ObservedAt: value.ObservedAt,
			Location: value.Location, FRP: value.FRP, Confidence: value.Confidence,
		})
		if err != nil {
			return processResult{}, err
		}
		links[value.ID] = item.ID
	}
	if _, err := tracker.Advance(now); err != nil {
		return processResult{}, err
	}
	incidents := tracker.List()
	router, err := alert.NewRouter(alertRules(cfg))
	if err != nil {
		return processResult{}, err
	}
	var alerts []alert.Envelope
	for _, item := range incidents {
		emitted, err := router.Route(item, now)
		if err != nil {
			return processResult{}, err
		}
		alerts = append(alerts, emitted...)
	}
	built, err := buildReport(fused.Observations, incidents, alerts, links, now)
	if err != nil {
		return processResult{}, err
	}
	return processResult{Ingest: loaded.Stats, Rejections: nonNilRejections(loaded.Rejections),
		Fusion:    fusionSummary{Observations: len(fused.Observations), DuplicateGroups: len(fused.DuplicateGroups)},
		Incidents: incidents, Alerts: nonNilAlerts(alerts), Report: built}, nil
}

func buildReport(values []fusion.FusedObservation, incidents []incident.Incident, alerts []alert.Envelope, links map[string]string, now time.Time) (report.Report, error) {
	start, end := values[0].ObservedAt, values[0].ObservedAt
	observations := make([]report.Observation, 0, len(values))
	for _, value := range values {
		if value.ObservedAt.Before(start) {
			start = value.ObservedAt
		}
		if value.ObservedAt.After(end) {
			end = value.ObservedAt
		}
		observations = append(observations, report.Observation{
			ID: value.ID, IncidentID: links[value.ID], Source: strings.Join(value.Sources, "+"),
			AcquiredAt: value.ObservedAt, Latitude: value.Location.Lat, Longitude: value.Location.Lon,
			FRP: value.FRP, Confidence: value.Confidence,
		})
	}
	severities := make(map[string]alert.Severity)
	for _, envelope := range alerts {
		if envelope.Severity > severities[envelope.IncidentID] {
			severities[envelope.IncidentID] = envelope.Severity
		}
	}
	projected := make([]report.Incident, 0, len(incidents))
	for _, item := range incidents {
		var closed *time.Time
		if !item.ClosedAt.IsZero() {
			value := item.ClosedAt
			closed = &value
		}
		projected = append(projected, report.Incident{
			ID: item.ID, Name: "Incident " + item.ID, Severity: reportSeverity(severities[item.ID]),
			Status: reportStatus(item.Status), StartedAt: item.FirstSeen, UpdatedAt: item.UpdatedAt,
			ClosedAt: closed, Sources: item.Sources,
		})
	}
	return report.Build(report.Input{GeneratedAt: now, Window: report.Window{Start: start, End: end}, Incidents: projected, Observations: observations})
}

func reportSeverity(value alert.Severity) report.Severity {
	switch value {
	case alert.Critical:
		return report.SeverityCritical
	case alert.Warning:
		return report.SeverityHigh
	case alert.Info:
		return report.SeverityLow
	default:
		return report.SeverityInformational
	}
}

func reportStatus(value incident.Status) report.Status {
	switch value {
	case incident.ActiveStatus:
		return report.StatusActive
	case incident.ContainedStatus:
		return report.StatusContained
	case incident.ClosedStatus:
		return report.StatusClosed
	default:
		return report.StatusDetected
	}
}

func nonNilRejections(values []ingest.Rejection) []ingest.Rejection {
	if values == nil {
		return []ingest.Rejection{}
	}
	return values
}

func nonNilAlerts(values []alert.Envelope) []alert.Envelope {
	if values == nil {
		return []alert.Envelope{}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].IncidentID != values[j].IncidentID {
			return values[i].IncidentID < values[j].IncidentID
		}
		if values[i].Channel != values[j].Channel {
			return values[i].Channel < values[j].Channel
		}
		return values[i].ID < values[j].ID
	})
	return values
}

func writeProcessOutput(w io.Writer, output string, result processResult) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("write JSON: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(w, "Pipeline\n  Accepted: %d\n  Rejected: %d\n  Fused observations: %d\n  Duplicate groups: %d\n  Alerts: %d\n\n",
		result.Ingest.Accepted, result.Ingest.Rejected, result.Fusion.Observations, result.Fusion.DuplicateGroups, len(result.Alerts)); err != nil {
		return err
	}
	return report.WriteText(w, result.Report)
}
