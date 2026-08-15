package fusion

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"firescope/geo"
	"firescope/model"
)

var testTime = time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)

func observation(id, satellite, instrument string, at time.Time, lat, lon float64) model.Observation {
	return model.Observation{
		ID: id, Satellite: satellite, Instrument: instrument, AcquiredAt: at,
		Latitude: lat, Longitude: lon, Brightness: 330, BrightnessT31: 290,
		FRP: 20, Confidence: 80, DayNight: model.Day, Scan: 1, Track: 1,
	}
}

func closeTo(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.12g, want %.12g (tolerance %.3g)", got, want, tolerance)
	}
}

func deduplicate(observations []model.Observation, config Config) ([]model.Observation, []DuplicateGroup, error) {
	engine, err := New(config)
	if err != nil {
		return nil, nil, err
	}
	return engine.Deduplicate(observations)
}

func TestConfigNormalizeAndValidate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupSourcePolicy = " SAME_SOURCE "
	cfg.SourceWeights = map[string]float64{" noaa-20 / viirs ": 0.8}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.DedupSourcePolicy != SameSource || cfg.SourceWeights["NOAA-20/VIIRS"] != 0.8 {
		t.Fatalf("config was not canonicalized: %#v", cfg)
	}

	bad := DefaultConfig()
	bad.DedupDistanceMeters = math.NaN()
	bad.HalfLife = 0
	bad.SourceWeights = map[string]float64{"broken": 2}
	err := bad.Validate()
	if err == nil || !strings.Contains(err.Error(), "dedup distance") ||
		!strings.Contains(err.Error(), "half-life") || !strings.Contains(err.Error(), "source weight key") {
		t.Fatalf("expected complete validation error, got %v", err)
	}
}

func TestDeduplicateWinnerAndGroup(t *testing.T) {
	cfg := DefaultConfig()
	cloudy := observation("cloudy", "NOAA-20", "VIIRS", testTime, 10, 20)
	cloudy.Confidence = 90
	cloudy.Flags = model.FlagCloud
	clean := observation("clean", "NOAA-20", "VIIRS", testTime.Add(time.Minute), 10.0001, 20)
	clean.Confidence = 80

	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cloudyScore, err := engine.Score(cloudy)
	if err != nil {
		t.Fatal(err)
	}
	cleanScore, err := engine.Score(clean)
	if err != nil {
		t.Fatal(err)
	}
	if cloudyScore.Reliability != 67.5 || cleanScore.Reliability != 80 || CompareWinnerScores(cleanScore, cloudyScore) >= 0 {
		t.Fatalf("winner scores do not explain selection: cloudy=%+v clean=%+v", cloudyScore, cleanScore)
	}
	copyConfig, err := engine.Config()
	if err != nil {
		t.Fatal(err)
	}
	copyConfig.FlagPenalties[model.FlagCloud] = 0
	unchanged, _ := engine.Config()
	if unchanged.FlagPenalties[model.FlagCloud] != 0.75 {
		t.Fatal("Config returned aliased maps")
	}

	winners, groups, err := engine.Deduplicate([]model.Observation{cloudy, clean})
	if err != nil {
		t.Fatalf("Deduplicate: %v", err)
	}
	if len(winners) != 1 || winners[0].ID != "clean" {
		t.Fatalf("unexpected winner: %#v", winners)
	}
	if len(groups) != 1 || groups[0].Winner.ID != "clean" || len(groups[0].Members) != 2 || len(groups[0].Losers) != 1 {
		t.Fatalf("unexpected group: %#v", groups)
	}
	if groups[0].Losers[0].ID != "cloudy" || !groups[0].Losers[0].Flags.Has(model.FlagDuplicate) {
		t.Fatalf("loser not explicitly marked: %#v", groups[0].Losers)
	}
}

func TestDedupThresholdsAreInclusive(t *testing.T) {
	a := observation("a", "S", "I", testTime, 0, 0)
	b := observation("b", "S", "I", testTime.Add(5*time.Minute), 0, 0.001)
	distance, err := geo.Haversine(geo.Point{Lat: a.Latitude, Lon: a.Longitude}, geo.Point{Lat: b.Latitude, Lon: b.Longitude})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DedupWindow = 5 * time.Minute
	cfg.DedupDistanceMeters = distance
	winners, groups, err := deduplicate([]model.Observation{a, b}, cfg)
	if err != nil || len(winners) != 1 || len(groups) != 1 {
		t.Fatalf("boundary values should group: winners=%d groups=%d err=%v", len(winners), len(groups), err)
	}

	b.AcquiredAt = b.AcquiredAt.Add(time.Nanosecond)
	winners, groups, err = deduplicate([]model.Observation{a, b}, cfg)
	if err != nil || len(winners) != 2 || len(groups) != 0 {
		t.Fatalf("time outside boundary grouped: winners=%d groups=%d err=%v", len(winners), len(groups), err)
	}
	b.AcquiredAt = testTime.Add(5 * time.Minute)
	cfg.DedupDistanceMeters = math.Nextafter(distance, 0)
	winners, groups, err = deduplicate([]model.Observation{a, b}, cfg)
	if err != nil || len(winners) != 2 || len(groups) != 0 {
		t.Fatalf("distance outside boundary grouped: winners=%d groups=%d err=%v", len(winners), len(groups), err)
	}
}

func TestFusionNumericsAndEvidence(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HalfLife = 10 * time.Minute
	old := observation("old", "SAT-A", "A", testTime, 0, 0)
	old.FRP, old.Confidence = 10, 50
	newer := observation("new", "SAT-B", "B", testTime.Add(10*time.Minute), 0, 0)
	newer.FRP, newer.Confidence = 30, 50

	result, err := Process([]model.Observation{newer, old}, cfg)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(result.Observations) != 1 {
		t.Fatalf("got %d fused observations", len(result.Observations))
	}
	got := result.Observations[0]
	closeTo(t, got.FRP, 70.0/3.0, 1e-10)
	closeTo(t, got.Confidence, 62.5, 1e-10)
	if got.ObservedAt != newer.AcquiredAt || len(got.Evidence) != 2 || len(got.Sources) != 2 {
		t.Fatalf("incomplete aggregate: %#v", got)
	}
	if got.Evidence[0].Observation.ID != "old" || got.Evidence[0].AgeFactor != 0.5 || got.Evidence[0].Weight != 0.5 {
		t.Fatalf("age decay not exposed correctly: %#v", got.Evidence)
	}
	if got.FRP < old.FRP || got.FRP > newer.FRP {
		t.Fatalf("weighted FRP outside evidence range: %g", got.FRP)
	}
}

func TestQualityAndSourceWeightsAffectFusion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SourceWeights = map[string]float64{"SAT-A/A": 0.5}
	a := observation("a", "SAT-A", "A", testTime, 1, 1)
	a.Flags = model.FlagWater
	a.Confidence = 100
	b := observation("b", "SAT-B", "B", testTime, 1, 1)
	b.Confidence = 0
	result, err := Process([]model.Observation{a, b}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Observations[0]
	closeTo(t, got.Evidence[0].QualityFactor, 0.5, 0)
	closeTo(t, got.Evidence[0].Weight, 0.25, 0)
	closeTo(t, got.Confidence, 25, 1e-12)
}

func TestSourcePoliciesConstrainStages(t *testing.T) {
	a := observation("a", "SAT", "INST", testTime, 0, 0)
	b := observation("b", "SAT", "INST", testTime.Add(time.Minute), 0, 0.001)
	cfg := DefaultConfig()
	cfg.DedupDistanceMeters = 1
	cfg.FusionDistanceMeters = 1_000

	result, err := Process([]model.Observation{a, b}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 2 {
		t.Fatalf("same source bypassed different-source fusion policy: %#v", result.Observations)
	}
	cfg.FusionSourcePolicy = AnySource
	result, err = Process([]model.Observation{a, b}, cfg)
	if err != nil || len(result.Observations) != 1 {
		t.Fatalf("any-source policy did not fuse: count=%d err=%v", len(result.Observations), err)
	}

	b.Satellite = "OTHER"
	cfg.FusionSourcePolicy = DifferentSource
	result, err = Process([]model.Observation{a, b}, cfg)
	if err != nil || len(result.Observations) != 1 {
		t.Fatalf("different sources did not fuse: count=%d err=%v", len(result.Observations), err)
	}
}

func TestDatelineSafeCentroid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FusionDistanceMeters = 30_000
	a := observation("east", "SAT-A", "A", testTime, 10, 179.9)
	b := observation("west", "SAT-B", "B", testTime, 10, -179.9)
	result, err := Process([]model.Observation{a, b}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fused := result.Observations
	if len(fused) != 1 {
		t.Fatalf("dateline observations not fused: %d", len(fused))
	}
	if math.Abs(math.Abs(fused[0].Location.Lon)-180) > 1e-6 {
		t.Fatalf("centroid crossed through Greenwich: %#v", fused[0].Location)
	}
	closeTo(t, fused[0].Location.Lat, 10.000014923, 1e-6)
}

func TestDuplicateEvidenceRetainedWithoutDoubleCounting(t *testing.T) {
	cfg := DefaultConfig()
	winner := observation("winner", "SAT-A", "A", testTime, 0, 0)
	winner.Confidence = 90
	loser := observation("loser", "SAT-A", "A", testTime.Add(time.Minute), 0, 0)
	loser.Confidence = 20
	other := observation("other", "SAT-B", "B", testTime, 0, 0)
	other.Confidence = 50
	result, err := Process([]model.Observation{loser, other, winner}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || len(result.DuplicateGroups) != 1 {
		t.Fatalf("unexpected result shape: %#v", result)
	}
	got := result.Observations[0]
	if len(got.Evidence) != 3 {
		t.Fatalf("loser missing from evidence: %#v", got.Evidence)
	}
	duplicates := 0
	for _, item := range got.Evidence {
		if item.Duplicate {
			duplicates++
			if item.Weight != 0 || !item.Observation.Flags.Has(model.FlagDuplicate) {
				t.Fatalf("duplicate contributed or was not flagged: %#v", item)
			}
		}
	}
	if duplicates != 1 {
		t.Fatalf("got %d duplicate evidence entries", duplicates)
	}
	closeTo(t, got.Confidence, 95, 1e-12)
}

func TestInputOrderDoesNotAffectResults(t *testing.T) {
	cfg := DefaultConfig()
	values := []model.Observation{
		observation("a", "SAT-A", "A", testTime, 20, 30),
		observation("a2", "SAT-A", "A", testTime.Add(time.Minute), 20.0001, 30),
		observation("b", "SAT-B", "B", testTime.Add(2*time.Minute), 20, 30.0001),
		observation("later", "SAT-C", "C", testTime.Add(time.Hour), -20, -30),
	}
	first, err := Process(values, cfg)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := []model.Observation{values[3], values[1], values[0], values[2]}
	second, err := Process(shuffled, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("order changed result:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if len(first.Observations) != 2 || first.Observations[0].ObservedAt.After(first.Observations[1].ObservedAt) {
		t.Fatalf("fused observations are not deterministically sorted: %#v", first.Observations)
	}
}

func TestStableIDUsesCanonicalFields(t *testing.T) {
	cfg := DefaultConfig()
	a := observation("evidence", " sat-a ", " viirs ", testTime.In(time.FixedZone("X", 3600)), 0, 180)
	b := observation("evidence", "SAT-A", "VIIRS", testTime, 0, -180)
	first, err := Process([]model.Observation{a}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Process([]model.Observation{b}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Observations[0].ID != second.Observations[0].ID {
		t.Fatalf("canonical equivalents got different IDs: %q != %q", first.Observations[0].ID, second.Observations[0].ID)
	}
}

func TestCompleteLinkPreventsThresholdChaining(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DedupDistanceMeters = 150
	a := observation("a", "S", "I", testTime, 0, 0)
	b := observation("b", "S", "I", testTime, 0, 0.001)
	c := observation("c", "S", "I", testTime, 0, 0.002)
	winners, groups, err := deduplicate([]model.Observation{c, b, a}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(winners) != 2 || len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("chain bridged observations beyond threshold: winners=%#v groups=%#v", winners, groups)
	}
}
