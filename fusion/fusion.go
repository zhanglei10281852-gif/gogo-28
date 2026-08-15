package fusion

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"time"

	"firescope/geo"
	"firescope/model"
)

type SourcePolicy string

const (
	AnySource       SourcePolicy = "any"
	SameSatellite   SourcePolicy = "same_satellite"
	SameInstrument  SourcePolicy = "same_instrument"
	SameSource      SourcePolicy = "same_source"
	DifferentSource SourcePolicy = "different_source"
)

type WinnerRule string

const HighestReliability WinnerRule = "highest_reliability"

type Config struct {
	DedupDistanceMeters  float64
	DedupWindow          time.Duration
	DedupSourcePolicy    SourcePolicy
	FusionDistanceMeters float64
	FusionWindow         time.Duration
	FusionSourcePolicy   SourcePolicy
	WinnerRule           WinnerRule
	HalfLife             time.Duration
	DefaultSourceWeight  float64
	SourceWeights        map[string]float64
	FlagPenalties        map[model.QualityFlag]float64
}

func DefaultConfig() Config {
	return Config{
		DedupDistanceMeters: 750, DedupWindow: 5 * time.Minute, DedupSourcePolicy: SameSource,
		FusionDistanceMeters: 2_000, FusionWindow: 20 * time.Minute, FusionSourcePolicy: DifferentSource,
		WinnerRule: HighestReliability, HalfLife: 30 * time.Minute, DefaultSourceWeight: 1,
		SourceWeights: map[string]float64{},
		FlagPenalties: map[model.QualityFlag]float64{
			model.FlagCloud: 0.75, model.FlagWater: 0.5, model.FlagSaturated: 0.8,
			model.FlagLowSignal: 0.6, model.FlagGeolocationSuspect: 0.4, model.FlagDuplicate: 0,
		},
	}
}

var qualityFlags = []model.QualityFlag{model.FlagCloud, model.FlagWater, model.FlagSaturated, model.FlagLowSignal, model.FlagGeolocationSuspect, model.FlagDuplicate}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (p SourcePolicy) valid() bool {
	return p == AnySource || p == SameSatellite || p == SameInstrument || p == SameSource || p == DifferentSource
}
func SourceKey(satellite, instrument string) string {
	return strings.ToUpper(strings.TrimSpace(satellite)) + "/" + strings.ToUpper(strings.TrimSpace(instrument))
}
func (c *Config) Normalize() error {
	if c == nil {
		return errors.New("fusion config is nil")
	}
	c.DedupSourcePolicy = SourcePolicy(strings.ToLower(strings.TrimSpace(string(c.DedupSourcePolicy))))
	c.FusionSourcePolicy = SourcePolicy(strings.ToLower(strings.TrimSpace(string(c.FusionSourcePolicy))))
	c.WinnerRule = WinnerRule(strings.ToLower(strings.TrimSpace(string(c.WinnerRule))))
	weights := make(map[string]float64, len(c.SourceWeights))
	for raw, value := range c.SourceWeights {
		parts := strings.Split(raw, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("invalid fusion config: source weight key %q must be SATELLITE/INSTRUMENT", raw)
		}
		key := SourceKey(parts[0], parts[1])
		if _, exists := weights[key]; exists {
			return fmt.Errorf("invalid fusion config: source weight keys collide after normalization at %q", key)
		}
		weights[key] = value
	}
	c.SourceWeights = weights
	return c.Validate()
}
func (c Config) Validate() error {
	checks := []struct {
		invalid bool
		message string
	}{
		{!finite(c.DedupDistanceMeters) || c.DedupDistanceMeters <= 0, "dedup distance must be positive and finite"},
		{c.DedupWindow <= 0, "dedup window must be positive"},
		{!c.DedupSourcePolicy.valid(), fmt.Sprintf("invalid dedup source policy %q", c.DedupSourcePolicy)},
		{!finite(c.FusionDistanceMeters) || c.FusionDistanceMeters <= 0, "fusion distance must be positive and finite"},
		{c.FusionWindow <= 0, "fusion window must be positive"},
		{!c.FusionSourcePolicy.valid(), fmt.Sprintf("invalid fusion source policy %q", c.FusionSourcePolicy)},
		{c.WinnerRule != HighestReliability, fmt.Sprintf("invalid winner rule %q", c.WinnerRule)},
		{c.HalfLife <= 0, "half-life must be positive"},
		{!finite(c.DefaultSourceWeight) || c.DefaultSourceWeight <= 0 || c.DefaultSourceWeight > 1, "default source weight must be finite and within (0,1]"},
	}
	var problems []string
	for _, check := range checks {
		if check.invalid {
			problems = append(problems, check.message)
		}
	}
	for key, value := range c.SourceWeights {
		parts := strings.Split(key, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || key != SourceKey(parts[0], parts[1]) {
			problems = append(problems, fmt.Sprintf("source weight key %q must be canonical SATELLITE/INSTRUMENT", key))
		}
		if !finite(value) || value <= 0 || value > 1 {
			problems = append(problems, fmt.Sprintf("source weight %q must be finite and within (0,1]", key))
		}
	}
	known := model.FlagCloud | model.FlagWater | model.FlagSaturated | model.FlagLowSignal | model.FlagGeolocationSuspect | model.FlagDuplicate
	for flag, value := range c.FlagPenalties {
		if flag == 0 || flag&^known != 0 || bits.OnesCount32(uint32(flag)) != 1 {
			problems = append(problems, fmt.Sprintf("quality penalty key 0x%X must be one known flag", uint32(flag)))
		}
		if !finite(value) || value < 0 || value > 1 {
			problems = append(problems, fmt.Sprintf("quality penalty 0x%X must be finite and within [0,1]", uint32(flag)))
		}
	}
	if len(problems) != 0 {
		return errors.New("invalid fusion config: " + strings.Join(problems, "; "))
	}
	return nil
}

type Evidence struct {
	Observation                      model.Observation
	Source                           string
	Weight, AgeFactor, QualityFactor float64
	Duplicate                        bool
}
type DuplicateGroup struct {
	ID              string
	Winner          model.Observation
	Members, Losers []model.Observation
}
type FusedObservation struct {
	ID              string
	ObservedAt      time.Time
	Location        geo.Point
	FRP, Confidence float64
	Flags           model.QualityFlag
	Sources         []string
	Evidence        []Evidence
}
type Result struct {
	Observations    []FusedObservation
	DuplicateGroups []DuplicateGroup
}
type WinnerScore struct {
	Observation                    model.Observation
	Reliability, SourceWeight      float64
	QualityFactor, Confidence, FRP float64
	FlagCount                      int
	AcquiredAt                     time.Time
}

func (s WinnerScore) Compare(other WinnerScore) int {
	return CompareWinnerScores(s, other)
}
func (s WinnerScore) Summary() string {
	return fmt.Sprintf("%s reliability=%.6f source=%.6f quality=%.6f confidence=%.6f flags=%d frp=%.6f acquired=%s", s.Observation.ID, s.Reliability, s.SourceWeight, s.QualityFactor, s.Confidence, s.FlagCount, s.FRP, s.AcquiredAt.UTC().Format(time.RFC3339Nano))
}

type Engine struct{ config Config }

func New(config Config) (*Engine, error) {
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return &Engine{config: config}, nil
}
func (e *Engine) Config() (Config, error) {
	if e == nil {
		return Config{}, errors.New("fusion engine is nil")
	}
	copy := e.config
	copy.SourceWeights = make(map[string]float64, len(e.config.SourceWeights))
	for key, value := range e.config.SourceWeights {
		copy.SourceWeights[key] = value
	}
	copy.FlagPenalties = make(map[model.QualityFlag]float64, len(e.config.FlagPenalties))
	for flag, value := range e.config.FlagPenalties {
		copy.FlagPenalties[flag] = value
	}
	return copy, nil
}
func Process(observations []model.Observation, config Config) (Result, error) {
	engine, err := New(config)
	if err != nil {
		return Result{}, err
	}
	return engine.Process(observations)
}
func (e *Engine) Process(observations []model.Observation) (Result, error) {
	if e == nil {
		return Result{}, errors.New("fusion engine is nil")
	}
	winners, groups, err := e.Deduplicate(observations)
	if err != nil {
		return Result{}, err
	}
	fused, err := e.fuse(winners, groups)
	if err != nil {
		return Result{}, err
	}
	return Result{Observations: fused, DuplicateGroups: groups}, nil
}
func (e *Engine) Deduplicate(observations []model.Observation) ([]model.Observation, []DuplicateGroup, error) {
	if e == nil {
		return nil, nil, errors.New("fusion engine is nil")
	}
	values, err := normalizeObservations(observations)
	if err != nil {
		return nil, nil, err
	}
	clusters, err := partition(values, e.config.DedupDistanceMeters, e.config.DedupWindow, e.config.DedupSourcePolicy)
	if err != nil {
		return nil, nil, err
	}
	winners := make([]model.Observation, 0, len(clusters))
	groups := make([]DuplicateGroup, 0)
	for _, members := range clusters {
		winner := e.chooseWinner(members)
		winners = append(winners, winner)
		if len(members) == 1 {
			continue
		}
		group := DuplicateGroup{Winner: winner, Members: append([]model.Observation(nil), members...)}
		for _, member := range members {
			if member.ID == winner.ID {
				continue
			}
			member.Flags |= model.FlagDuplicate
			group.Losers = append(group.Losers, member)
		}
		group.ID = stableGroupID("dup", members)
		groups = append(groups, group)
	}
	model.SortObservations(winners)
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return winners, groups, nil
}
func normalizeObservations(input []model.Observation) ([]model.Observation, error) {
	values := append([]model.Observation(nil), input...)
	seen := make(map[string]int, len(values))
	for i := range values {
		if err := values[i].Normalize(); err != nil {
			return nil, fmt.Errorf("observation %d: %w", i, err)
		}
		if previous, exists := seen[values[i].ID]; exists {
			return nil, fmt.Errorf("duplicate observation ID %q at indexes %d and %d", values[i].ID, previous, i)
		}
		seen[values[i].ID] = i
	}
	model.SortObservations(values)
	return values, nil
}
func partition(values []model.Observation, distance float64, window time.Duration, policy SourcePolicy) ([][]model.Observation, error) {
	var clusters [][]model.Observation
	for _, value := range values {
		placed := false
		for i := range clusters {
			eligible := true
			for _, member := range clusters[i] {
				if !sourcesMatch(value, member, policy) || value.AcquiredAt.Sub(member.AcquiredAt).Abs() > window {
					eligible = false
					break
				}
				meters, err := geo.Haversine(point(value), point(member))
				if err != nil {
					return nil, err
				}
				if meters > distance {
					eligible = false
					break
				}
			}
			if eligible {
				clusters[i] = append(clusters[i], value)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, []model.Observation{value})
		}
	}
	return clusters, nil
}
func sourcesMatch(a, b model.Observation, policy SourcePolicy) bool {
	switch policy {
	case AnySource:
		return true
	case SameSatellite:
		return a.Satellite == b.Satellite
	case SameInstrument:
		return a.Instrument == b.Instrument
	case SameSource:
		return a.Satellite == b.Satellite && a.Instrument == b.Instrument
	case DifferentSource:
		return a.Satellite != b.Satellite || a.Instrument != b.Instrument
	default:
		return false
	}
}
func point(o model.Observation) geo.Point { return geo.Point{Lat: o.Latitude, Lon: o.Longitude} }
func (e *Engine) sourceWeight(o model.Observation) float64 {
	if value, ok := e.config.SourceWeights[SourceKey(o.Satellite, o.Instrument)]; ok {
		return value
	}
	return e.config.DefaultSourceWeight
}
func (e *Engine) qualityFactor(flags model.QualityFlag) float64 {
	factor := 1.0
	for _, flag := range qualityFlags {
		if flags.Has(flag) {
			if penalty, exists := e.config.FlagPenalties[flag]; exists {
				factor *= penalty
			}
		}
	}
	return factor
}

// Score exposes every factor used by the deterministic winner rule.
func (e *Engine) Score(observation model.Observation) (WinnerScore, error) {
	if e == nil {
		return WinnerScore{}, errors.New("fusion engine is nil")
	}
	if err := observation.Normalize(); err != nil {
		return WinnerScore{}, fmt.Errorf("score observation: %w", err)
	}
	return e.score(observation), nil
}
func (e *Engine) score(observation model.Observation) WinnerScore {
	sourceWeight := e.sourceWeight(observation)
	qualityFactor := e.qualityFactor(observation.Flags)
	return WinnerScore{
		Observation: observation, Reliability: sourceWeight * qualityFactor * observation.Confidence,
		SourceWeight: sourceWeight, QualityFactor: qualityFactor, Confidence: observation.Confidence,
		FRP: observation.FRP, FlagCount: bits.OnesCount32(uint32(observation.Flags)),
		AcquiredAt: observation.AcquiredAt,
	}
}

// CompareWinnerScores returns -1 when a outranks b, 1 when b outranks a,
// and zero only for equivalent canonical observations.
func CompareWinnerScores(a, b WinnerScore) int {
	greater := func(left, right float64) int {
		if left > right {
			return -1
		}
		if left < right {
			return 1
		}
		return 0
	}
	if cmp := greater(a.Reliability, b.Reliability); cmp != 0 {
		return cmp
	}
	if cmp := greater(a.Confidence, b.Confidence); cmp != 0 {
		return cmp
	}
	if a.FlagCount != b.FlagCount {
		if a.FlagCount < b.FlagCount {
			return -1
		}
		return 1
	}
	if cmp := greater(a.FRP, b.FRP); cmp != 0 {
		return cmp
	}
	if cmp := a.AcquiredAt.Compare(b.AcquiredAt); cmp != 0 {
		return -cmp
	}
	return model.CompareObservations(a.Observation, b.Observation)
}

// HighestReliability compares sourceWeight*qualityFactor*confidence, then raw
// confidence, fewer flag bits, greater FRP, newer time, and canonical order.
func (e *Engine) chooseWinner(members []model.Observation) model.Observation {
	best := e.score(members[0])
	for _, candidate := range members[1:] {
		score := e.score(candidate)
		if CompareWinnerScores(score, best) < 0 {
			best = score
		}
	}
	return best.Observation
}
func (e *Engine) fuse(winners []model.Observation, groups []DuplicateGroup) ([]FusedObservation, error) {
	clusters, err := partition(winners, e.config.FusionDistanceMeters, e.config.FusionWindow, e.config.FusionSourcePolicy)
	if err != nil {
		return nil, err
	}
	losers := make(map[string][]model.Observation)
	for _, group := range groups {
		losers[group.Winner.ID] = append([]model.Observation(nil), group.Losers...)
	}
	result := make([]FusedObservation, 0, len(clusters))
	for _, members := range clusters {
		fused := e.aggregate(members, losers)
		result = append(result, fused)
	}
	sort.Slice(result, func(i, j int) bool {
		if cmp := result[i].ObservedAt.Compare(result[j].ObservedAt); cmp != 0 {
			return cmp < 0
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}
func (e *Engine) aggregate(members []model.Observation, losers map[string][]model.Observation) FusedObservation {
	reference := members[0].AcquiredAt
	for _, member := range members[1:] {
		if member.AcquiredAt.After(reference) {
			reference = member.AcquiredAt
		}
	}
	var x, y, z, totalWeight, frpTotal, missProbability float64
	missProbability = 1
	flags := model.QualityFlag(0)
	sourceSet := make(map[string]struct{})
	evidence := make([]Evidence, 0, len(members))
	for _, member := range members {
		ageFactor := math.Exp(-math.Ln2 * reference.Sub(member.AcquiredAt).Seconds() / e.config.HalfLife.Seconds())
		qualityFactor := e.qualityFactor(member.Flags)
		weight := e.sourceWeight(member) * ageFactor * qualityFactor
		lat, lon := member.Latitude*math.Pi/180, member.Longitude*math.Pi/180
		x += weight * math.Cos(lat) * math.Cos(lon)
		y += weight * math.Cos(lat) * math.Sin(lon)
		z += weight * math.Sin(lat)
		totalWeight += weight
		frpTotal += weight * member.FRP
		adjusted := math.Min(1, math.Max(0, member.Confidence/100*weight))
		missProbability *= 1 - adjusted
		flags |= member.Flags
		source := SourceKey(member.Satellite, member.Instrument)
		sourceSet[source] = struct{}{}
		evidence = append(evidence, Evidence{Observation: member, Source: source, Weight: weight, AgeFactor: ageFactor, QualityFactor: qualityFactor})
		for _, loser := range losers[member.ID] {
			loser.Flags |= model.FlagDuplicate
			flags |= loser.Flags
			evidence = append(evidence, Evidence{Observation: loser, Source: SourceKey(loser.Satellite, loser.Instrument), Duplicate: true})
		}
	}
	location := point(members[0])
	if totalWeight > 0 {
		norm := math.Sqrt(x*x + y*y + z*z)
		if norm > 1e-15 {
			location.Lat = math.Atan2(z, math.Sqrt(x*x+y*y)) * 180 / math.Pi
			location.Lon = math.Atan2(y, x) * 180 / math.Pi
			location, _ = geo.NormalizePoint(location)
		}
	}
	frp := 0.0
	if totalWeight > 0 {
		frp = frpTotal / totalWeight
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	sort.Slice(evidence, func(i, j int) bool {
		a, b := evidence[i].Observation, evidence[j].Observation
		if cmp := model.CompareObservations(a, b); cmp != 0 {
			return cmp < 0
		}
		if evidence[i].Duplicate != evidence[j].Duplicate {
			return !evidence[i].Duplicate
		}
		return evidence[i].Source < evidence[j].Source
	})
	all := make([]model.Observation, len(evidence))
	for i := range evidence {
		all[i] = evidence[i].Observation
	}
	return FusedObservation{
		ID: stableGroupID("fused", all), ObservedAt: reference, Location: location,
		FRP: frp, Confidence: 100 * (1 - missProbability), Flags: flags,
		Sources: sources, Evidence: evidence,
	}
}
func stableGroupID(prefix string, members []model.Observation) string {
	model.SortObservations(members)
	h := sha256.New()
	for _, value := range members {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%.7f\x00%.7f\x00%.6f\x00%.6f\x00%.6f\x00%.6f\x00%s\x00%.6f\x00%.6f\x00%d\n",
			value.Satellite, value.Instrument, value.AcquiredAt.UTC().UnixNano(),
			value.Latitude, value.Longitude, value.Brightness, value.BrightnessT31,
			value.FRP, value.Confidence, value.DayNight, value.Scan, value.Track, value.Flags)
	}
	return fmt.Sprintf("%s-%x", prefix, h.Sum(nil)[:12])
}
