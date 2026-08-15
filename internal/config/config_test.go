package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultsValidate(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Dedup.TimeWindow.Value() != 10*time.Minute {
		t.Fatal("unexpected default")
	}
}

func TestStrictLoadAndOverride(t *testing.T) {
	cfg, err := Load(strings.NewReader(`{"dedup":{"distance_meters":750,"time_window":"5m","sources":["viirs","modis"]},"fusion":{"enabled":true,"source_weights":{"viirs":0.7,"modis":0.3},"max_age":"20m","minimum_sources":2},"incident":{"merge_distance_meters":2500,"quiet_period":"2h","minimum_observations":2,"maximum_duration":"48h"},"alert":{"enabled":true,"minimum_frp":10,"minimum_confidence":70,"cooldown":"30m","channels":["ops"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dedup.DistanceMeters != 750 || cfg.Fusion.MinimumSources != 2 {
		t.Fatalf("bad config: %+v", cfg)
	}
}

func TestLoadRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		`{"unknown":1}`, `{} {}`, `{"dedup":{"time_window":5}}`,
	} {
		if _, err := Load(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestCrossFieldValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"weights", func(c *Config) { c.Fusion.SourceWeights["VIIRS"] = .9 }, "sum to 1"},
		{"fusion count", func(c *Config) { c.Fusion.MinimumSources = 3 }, "weighted source count"},
		{"incident distance", func(c *Config) { c.Incident.MergeDistanceMeters = 100 }, "must not be less"},
		{"incident duration", func(c *Config) { c.Incident.MaximumDuration = Duration(time.Hour) }, "must exceed"},
		{"alert confidence", func(c *Config) { c.Alert.MinimumConfidence = 101 }, "minimum_confidence"},
		{"alert channels", func(c *Config) { c.Alert.Channels = nil }, "channels is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestNormalizeCollections(t *testing.T) {
	cfg := Defaults()
	cfg.Dedup.Sources = []string{" viirs", "VIIRS", "modis"}
	cfg.Alert.Channels = []string{" Ops ", "ops", "Email"}
	cfg.Fusion.SourceWeights = map[string]float64{"viirs": .3, " VIIRS ": .3, "modis": .4}
	cfg.Normalize()
	if len(cfg.Dedup.Sources) != 2 || len(cfg.Alert.Channels) != 2 || cfg.Fusion.SourceWeights["VIIRS"] != .6 {
		t.Fatalf("not normalized: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
