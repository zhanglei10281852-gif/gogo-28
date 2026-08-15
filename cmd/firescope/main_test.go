package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const cliObservations = `[
 {"id":"a","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:00:00Z","latitude":34,"longitude":-118,"brightness":330,"brightness_t31":290,"frp":10,"confidence":70,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"a-better","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:01:00Z","latitude":34.0001,"longitude":-118,"brightness":335,"brightness_t31":291,"frp":20,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"b","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:20:00Z","latitude":34.0002,"longitude":-118,"brightness":332,"brightness_t31":290,"frp":15,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]},
 {"id":"c","satellite":"AQUA","instrument":"MODIS","acquired_at":"2026-07-07T12:20:30Z","latitude":34.0003,"longitude":-118,"brightness":334,"brightness_t31":290,"frp":18,"confidence":100,"day_night":"D","scan":1,"track":1,"quality_flags":[]}
]`

func TestRunProcessJSONSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"process", "--input", "-", "--format", "json", "--output", "json", "--now", "2026-07-07T12:20:30Z"}, strings.NewReader(cliObservations), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var output struct {
		Fusion fusionSummary `json:"fusion"`
		Alerts []any         `json:"alerts"`
		Report struct {
			Summary struct {
				IncidentCount int `json:"incident_count"`
			} `json:"summary"`
		} `json:"report"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Fusion.DuplicateGroups != 1 || output.Fusion.Observations != 2 || output.Report.Summary.IncidentCount != 1 || len(output.Alerts) != 1 {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestRunErrorsUseStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"process", "--format", "yaml"}, strings.NewReader("[]"), &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid format") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"missing"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunSnapshotInspectListRoundTrip(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	batch := `{"orbit_id":"orbit-7","satellite":"NOAA-20","window_start":"","window_end":"","observations":[{"id":"one","satellite":"NOAA-20","instrument":"VIIRS","acquired_at":"2026-07-07T12:00:00Z","latitude":34,"longitude":-118,"brightness":330,"brightness_t31":290,"frp":10,"confidence":70,"day_night":"D","scan":1,"track":1,"quality_flags":[]}]}`
	var stdout, stderr bytes.Buffer
	code := run([]string{"snapshot", "--input", "-", "--format", "json", "--store", directory, "--name", "daily", "--now", "2026-07-07T12:20:30Z", "--output", "json"}, strings.NewReader(batch), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("snapshot code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	code = run([]string{"inspect", "--store", directory, "--name", "daily", "--output", "json"}, nil, &stdout, &stderr)
	if code != 0 || !json.Valid(stdout.Bytes()) || !bytes.Contains(stdout.Bytes(), []byte(`"observation_count":1`)) {
		t.Fatalf("inspect code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	code = run([]string{"list", "--store", directory, "--output", "text"}, nil, &stdout, &stderr)
	if code != 0 || stdout.String() != "daily\n" {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestHelpAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, nil, &stdout, &stderr); code != 0 || stdout.String() != version+"\n" || stderr.Len() != 0 {
		t.Fatalf("version output=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"process", "--help"}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "--config") {
		t.Fatalf("help code=%d output=%q", code, stdout.String())
	}
}
