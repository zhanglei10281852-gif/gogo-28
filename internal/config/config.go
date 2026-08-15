package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("cannot decode duration into nil receiver")
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("duration must be a quoted Go duration")
	}
	value, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(value)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type DedupConfig struct {
	Enabled        bool     `json:"enabled"`
	DistanceMeters float64  `json:"distance_meters"`
	TimeWindow     Duration `json:"time_window"`
	Sources        []string `json:"sources"`
}

type FusionConfig struct {
	Enabled        bool               `json:"enabled"`
	SourceWeights  map[string]float64 `json:"source_weights"`
	MaxAge         Duration           `json:"max_age"`
	MinimumSources int                `json:"minimum_sources"`
}

type IncidentConfig struct {
	MergeDistanceMeters float64  `json:"merge_distance_meters"`
	QuietPeriod         Duration `json:"quiet_period"`
	MinimumObservations int      `json:"minimum_observations"`
	MaximumDuration     Duration `json:"maximum_duration"`
}

type AlertConfig struct {
	Enabled           bool     `json:"enabled"`
	MinimumFRP        float64  `json:"minimum_frp"`
	MinimumConfidence float64  `json:"minimum_confidence"`
	Cooldown          Duration `json:"cooldown"`
	Channels          []string `json:"channels"`
}

type Config struct {
	Dedup    DedupConfig    `json:"dedup"`
	Fusion   FusionConfig   `json:"fusion"`
	Incident IncidentConfig `json:"incident"`
	Alert    AlertConfig    `json:"alert"`
}

func Defaults() Config {
	return Config{
		Dedup:    DedupConfig{Enabled: true, DistanceMeters: 500, TimeWindow: Duration(10 * time.Minute), Sources: []string{"VIIRS", "MODIS"}},
		Fusion:   FusionConfig{Enabled: true, SourceWeights: map[string]float64{"VIIRS": 0.6, "MODIS": 0.4}, MaxAge: Duration(30 * time.Minute), MinimumSources: 2},
		Incident: IncidentConfig{MergeDistanceMeters: 3000, QuietPeriod: Duration(6 * time.Hour), MinimumObservations: 2, MaximumDuration: Duration(7 * 24 * time.Hour)},
		Alert:    AlertConfig{Enabled: true, MinimumFRP: 5, MinimumConfidence: 60, Cooldown: Duration(1 * time.Hour), Channels: []string{"default"}},
	}
}

func Load(reader io.Reader) (Config, error) {
	if reader == nil {
		return Config{}, errors.New("config reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	config := Defaults()
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) == nil {
		var fusion map[string]json.RawMessage
		if raw, ok := root["fusion"]; ok && json.Unmarshal(raw, &fusion) == nil {
			if _, replacesWeights := fusion["source_weights"]; replacesWeights {
				config.Fusion.SourceWeights = nil
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("decode config: trailing JSON value")
		}
		return Config{}, fmt.Errorf("decode config trailer: %w", err)
	}
	config.Normalize()
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func LoadBytes(data []byte) (Config, error) { return Load(bytes.NewReader(data)) }

func LoadFile(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()
	config, err := Load(file)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}
	return config, nil
}

func normalizeNames(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (c *Config) Normalize() {
	if c == nil {
		return
	}
	c.Dedup.Sources = normalizeNames(c.Dedup.Sources)
	channels := make([]string, 0, len(c.Alert.Channels))
	seen := make(map[string]struct{}, len(c.Alert.Channels))
	for _, channel := range c.Alert.Channels {
		channel = strings.TrimSpace(channel)
		if channel == "" {
			continue
		}
		key := strings.ToLower(channel)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		channels = append(channels, channel)
	}
	sort.Slice(channels, func(i, j int) bool { return strings.ToLower(channels[i]) < strings.ToLower(channels[j]) })
	c.Alert.Channels = channels
	weights := make(map[string]float64, len(c.Fusion.SourceWeights))
	for source, weight := range c.Fusion.SourceWeights {
		weights[strings.ToUpper(strings.TrimSpace(source))] += weight
	}
	c.Fusion.SourceWeights = weights
}

func positiveFinite(name string, value float64, maximum float64, problems *[]string) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > maximum {
		*problems = append(*problems, fmt.Sprintf("%s must be finite and within (0,%g]", name, maximum))
	}
}

func (c Config) Validate() error {
	var problems []string
	positiveFinite("dedup.distance_meters", c.Dedup.DistanceMeters, 100000, &problems)
	if c.Dedup.TimeWindow <= 0 || c.Dedup.TimeWindow > Duration(24*time.Hour) {
		problems = append(problems, "dedup.time_window must be within (0,24h]")
	}
	if c.Dedup.Enabled && len(c.Dedup.Sources) == 0 {
		problems = append(problems, "dedup.sources is required when dedup is enabled")
	}
	seenSources := make(map[string]struct{}, len(c.Dedup.Sources))
	for i, source := range c.Dedup.Sources {
		canonical := strings.ToUpper(strings.TrimSpace(source))
		if canonical == "" {
			problems = append(problems, fmt.Sprintf("dedup.sources[%d] is empty", i))
			continue
		}
		if _, exists := seenSources[canonical]; exists {
			problems = append(problems, fmt.Sprintf("dedup.sources contains duplicate %q", canonical))
		}
		seenSources[canonical] = struct{}{}
	}
	if c.Fusion.MaxAge <= 0 || c.Fusion.MaxAge > Duration(7*24*time.Hour) {
		problems = append(problems, "fusion.max_age must be within (0,168h]")
	}
	if c.Fusion.MinimumSources < 1 {
		problems = append(problems, "fusion.minimum_sources must be at least 1")
	}
	weightSum := 0.0
	for source, weight := range c.Fusion.SourceWeights {
		if strings.TrimSpace(source) == "" {
			problems = append(problems, "fusion source name is empty")
		}
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 || weight > 1 {
			problems = append(problems, fmt.Sprintf("fusion weight for %q must be within (0,1]", source))
		}
		weightSum += weight
	}
	if c.Fusion.Enabled {
		if len(c.Fusion.SourceWeights) < c.Fusion.MinimumSources {
			problems = append(problems, "fusion.minimum_sources exceeds weighted source count")
		}
		if math.Abs(weightSum-1) > 1e-9 {
			problems = append(problems, fmt.Sprintf("fusion source weights must sum to 1, got %.12g", weightSum))
		}
		if c.Fusion.MaxAge < c.Dedup.TimeWindow {
			problems = append(problems, "fusion.max_age must not be shorter than dedup.time_window")
		}
	}
	positiveFinite("incident.merge_distance_meters", c.Incident.MergeDistanceMeters, 1000000, &problems)
	if c.Incident.QuietPeriod <= 0 {
		problems = append(problems, "incident.quiet_period must be positive")
	}
	if c.Incident.MinimumObservations < 1 {
		problems = append(problems, "incident.minimum_observations must be at least 1")
	}
	if c.Incident.MaximumDuration <= c.Incident.QuietPeriod {
		problems = append(problems, "incident.maximum_duration must exceed quiet_period")
	}
	if c.Incident.MergeDistanceMeters < c.Dedup.DistanceMeters {
		problems = append(problems, "incident.merge_distance_meters must not be less than dedup.distance_meters")
	}
	if math.IsNaN(c.Alert.MinimumFRP) || math.IsInf(c.Alert.MinimumFRP, 0) || c.Alert.MinimumFRP < 0 {
		problems = append(problems, "alert.minimum_frp must be finite and non-negative")
	}
	if math.IsNaN(c.Alert.MinimumConfidence) || math.IsInf(c.Alert.MinimumConfidence, 0) || c.Alert.MinimumConfidence < 0 || c.Alert.MinimumConfidence > 100 {
		problems = append(problems, "alert.minimum_confidence must be within [0,100]")
	}
	if c.Alert.Cooldown < 0 || c.Alert.Cooldown > c.Incident.MaximumDuration {
		problems = append(problems, "alert.cooldown must be non-negative and no longer than incident.maximum_duration")
	}
	if c.Alert.Enabled && len(c.Alert.Channels) == 0 {
		problems = append(problems, "alert.channels is required when alerts are enabled")
	}
	seenChannels := make(map[string]struct{}, len(c.Alert.Channels))
	for _, channel := range c.Alert.Channels {
		key := strings.ToLower(strings.TrimSpace(channel))
		if key == "" {
			problems = append(problems, "alert channel is empty")
			continue
		}
		if _, exists := seenChannels[key]; exists {
			problems = append(problems, fmt.Sprintf("alert channel %q is duplicated", channel))
		}
		seenChannels[key] = struct{}{}
	}
	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	return nil
}
