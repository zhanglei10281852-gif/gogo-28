package ingest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"firescope/model"
)

func observationJSON(id, timestamp string) string {
	return fmt.Sprintf(`{
		"id":%q,"satellite":" noaa-20 ","instrument":"viirs","acquired_at":%q,
		"latitude":10,"longitude":180,"brightness":330,"brightness_t31":290,
		"frp":12.5,"confidence":80,"day_night":"d","scan":0.4,"track":0.5,
		"quality_flags":["cloud","WATER"]}`, id, timestamp)
}

func csvHeader() string {
	return "satellite,instrument,acquired_at,latitude,longitude,brightness,brightness_t31,frp,confidence,day_night,scan,track,quality_flags\n"
}

func csvRecord(timestamp string) string {
	return "noaa-20,viirs," + timestamp + ",10,20,330,290,12.5,80,D,0.4,0.5,cloud|water\n"
}

func TestJSONFormsRejectionsAndSorting(t *testing.T) {
	input := "[" + observationJSON("later", "2025-01-02T03:05:00+01:00") + "," +
		observationJSON("earlier", "2025-01-02T01:04:00Z") + `,{"satellite":"x","unknown":1}]`
	result, err := Ingest(strings.NewReader(input), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats != (Stats{Records: 3, Accepted: 2, Rejected: 1}) {
		t.Fatalf("stats = %+v", result.Stats)
	}
	if len(result.Rejections) != 1 || !strings.Contains(result.Rejections[0].Reason, "unknown field") {
		t.Fatalf("rejections = %+v", result.Rejections)
	}
	if result.Observations[0].ID != "earlier" || result.Observations[1].Satellite != "NOAA-20" {
		t.Fatalf("observations not normalized and sorted: %+v", result.Observations)
	}
	if !result.Observations[0].Flags.Has(model.FlagCloud | model.FlagWater) {
		t.Fatal("quality flags were not parsed")
	}

	single, err := Ingest(strings.NewReader(observationJSON("one", "2025-01-02T03:04:00Z")), Options{Format: FormatJSON})
	if err != nil || len(single.Observations) != 1 {
		t.Fatalf("single result=%+v err=%v", single, err)
	}
	if _, err := Ingest(strings.NewReader(observationJSON("x", "2025-01-02T03:04:00Z")+" true"), Options{}); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing error = %v", err)
	}
}

func TestOrbitBatchNormalization(t *testing.T) {
	input := fmt.Sprintf(`{
		"orbit_id":" orbit-7 ","satellite":"noaa-20","window_start":"","window_end":"",
		"observations":[%s,%s]}`,
		observationJSON("b", "2025-01-02T03:05:00Z"),
		observationJSON("a", "2025-01-02T03:04:00Z"))
	result, err := Ingest(strings.NewReader(input), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats != (Stats{Records: 2, Accepted: 2, Batches: 1}) || len(result.Batches) != 1 {
		t.Fatalf("result = %+v", result)
	}
	batch := result.Batches[0]
	if batch.OrbitID != "orbit-7" || batch.Observations[0].ID != "a" || batch.WindowStart.IsZero() || batch.WindowEnd.IsZero() {
		t.Fatalf("batch not normalized: %+v", batch)
	}
}

func TestOrbitBatchRejectsBadMembers(t *testing.T) {
	badFlag := strings.Replace(observationJSON("bad", "2025-01-02T03:04:00Z"), `"cloud","WATER"`, `"bogus"`, 1)
	duplicate := observationJSON("ok", "2025-01-02T03:04:00Z")
	input := fmt.Sprintf(`{"orbit_id":"o","satellite":"NOAA-20","window_start":"2025-01-02T03:00:00Z","window_end":"2025-01-02T03:10:00Z","observations":[%s,%s,%s]}`,
		observationJSON("ok", "2025-01-02T03:04:00Z"), badFlag, duplicate)
	result, err := Ingest(strings.NewReader(input), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats != (Stats{Records: 3, Accepted: 1, Rejected: 2, Batches: 1}) || len(result.Rejections) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCSVNameMappingFlagsAndAutoDetect(t *testing.T) {
	input := "track,quality_flags,day_night,confidence,frp,brightness_t31,brightness,longitude,latitude,acquired_at,instrument,satellite,scan,id\n" +
		"0.5,cloud|water,d,80,12.5,290,330,20,10,2025-01-02T03:04:00+01:00,viirs,noaa-20,0.4,csv-1\n"
	result, err := Ingest(strings.NewReader(input), Options{Format: FormatAuto})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Accepted != 1 || result.Observations[0].ID != "csv-1" || result.Observations[0].AcquiredAt.Format("15:04") != "02:04" {
		t.Fatalf("result = %+v", result)
	}
	format, err := DetectFormat([]byte(" \n[{}]"))
	if err != nil || format != FormatJSON {
		t.Fatalf("format=%q err=%v", format, err)
	}
}

func TestCSVHeaderRules(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"missing", strings.Replace(csvHeader(), ",track", "", 1), "missing required"},
		{"unknown", strings.Replace(csvHeader(), "quality_flags", "mystery", 1), "unknown header"},
		{"duplicate", strings.Replace(csvHeader(), "quality_flags", "track", 1), "duplicate header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Ingest(strings.NewReader(tc.header), Options{Format: FormatCSV})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCSVPhysicalLineAndRecordRejection(t *testing.T) {
	input := csvHeader() + `"noaa-
20",viirs,not-a-time,10,20,330,290,12.5,80,D,0.4,0.5,cloud` + "\n" +
		csvRecord("2025-01-02T03:04:00Z")
	result, err := Ingest(strings.NewReader(input), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats != (Stats{Records: 2, Accepted: 1, Rejected: 1}) {
		t.Fatalf("stats = %+v", result.Stats)
	}
	if got := result.Rejections[0].Line; got != 3 {
		t.Fatalf("physical line = %d, want 3; rejection=%+v", got, result.Rejections[0])
	}
}

func TestLimitsInvalidInputsAndStream(t *testing.T) {
	if _, err := Ingest(nil, Options{}); !errors.Is(err, ErrNilReader) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := Ingest(strings.NewReader(" \r\n"), Options{}); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("empty error = %v", err)
	}
	if _, err := Ingest(strings.NewReader("[]"), Options{Format: "yaml"}); err == nil {
		t.Fatal("invalid format accepted")
	}
	if _, err := Ingest(strings.NewReader("[]"), Options{MaxRecords: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
	input := "[" + observationJSON("a", "2025-01-02T03:04:00Z") + "," + observationJSON("b", "2025-01-02T03:05:00Z") + "]"
	result, err := Ingest(strings.NewReader(input), Options{MaxRecords: 1})
	if !errors.Is(err, ErrRecordLimit) || result.Stats.Accepted != 1 {
		t.Fatalf("limited result=%+v err=%v", result, err)
	}
	if _, _, err := Stream(strings.NewReader(input), Options{}, nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	var ids []string
	stats, rejected, err := Stream(strings.NewReader(input), Options{}, func(observation model.Observation) error {
		ids = append(ids, observation.ID)
		return nil
	})
	if err != nil || stats.Accepted != 2 || len(rejected) != 0 || strings.Join(ids, ",") != "a,b" {
		t.Fatalf("stats=%+v rejected=%+v ids=%v err=%v", stats, rejected, ids, err)
	}
}
