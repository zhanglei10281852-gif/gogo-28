package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type OrbitBatch struct {
	OrbitID      string
	Satellite    string
	WindowStart  time.Time
	WindowEnd    time.Time
	Observations []Observation
}

func NewOrbitBatch(orbitID, satellite string, observations []Observation) (OrbitBatch, error) {
	batch := OrbitBatch{OrbitID: orbitID, Satellite: satellite, Observations: observations}
	if err := batch.Normalize(); err != nil {
		return OrbitBatch{}, err
	}
	return batch, nil
}

func (b *OrbitBatch) Normalize() error {
	if b == nil {
		return errors.New("orbit batch is nil")
	}
	b.OrbitID = strings.TrimSpace(b.OrbitID)
	b.Satellite = strings.ToUpper(strings.TrimSpace(b.Satellite))
	if b.WindowStart.IsZero() != b.WindowEnd.IsZero() {
		return errors.New("window start and end must both be set")
	}
	if !b.WindowStart.IsZero() {
		b.WindowStart = b.WindowStart.UTC()
		b.WindowEnd = b.WindowEnd.UTC()
	}
	seen := make(map[string]int, len(b.Observations))
	for i := range b.Observations {
		if err := b.Observations[i].Normalize(); err != nil {
			return fmt.Errorf("observation %d: %w", i, err)
		}
		observation := &b.Observations[i]
		if b.Satellite == "" {
			b.Satellite = observation.Satellite
		}
		if observation.Satellite != b.Satellite {
			return fmt.Errorf("observation %d satellite %q differs from batch satellite %q", i, observation.Satellite, b.Satellite)
		}
		if previous, exists := seen[observation.ID]; exists {
			return fmt.Errorf("duplicate observation ID %q at indexes %d and %d", observation.ID, previous, i)
		}
		seen[observation.ID] = i
	}
	SortObservations(b.Observations)
	if len(b.Observations) > 0 {
		first := b.Observations[0].AcquiredAt
		last := b.Observations[len(b.Observations)-1].AcquiredAt
		if b.WindowStart.IsZero() {
			b.WindowStart, b.WindowEnd = first, last
		}
		if first.Before(b.WindowStart) || last.After(b.WindowEnd) {
			return fmt.Errorf("observations span %s to %s outside batch window %s to %s", first.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano), b.WindowStart.Format(time.RFC3339Nano), b.WindowEnd.Format(time.RFC3339Nano))
		}
	}
	return b.Validate()
}

func (b OrbitBatch) Validate() error {
	var problems []string
	if b.OrbitID == "" {
		problems = append(problems, "orbit ID is required")
	}
	if b.Satellite == "" {
		problems = append(problems, "satellite is required")
	}
	if b.WindowStart.IsZero() || b.WindowEnd.IsZero() {
		problems = append(problems, "window is required")
	} else if b.WindowEnd.Before(b.WindowStart) {
		problems = append(problems, "window end precedes start")
	}
	for i, observation := range b.Observations {
		if err := observation.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("observation %d: %v", i, err))
		}
		if i > 0 && CompareObservations(b.Observations[i-1], observation) > 0 {
			problems = append(problems, "observations are not deterministically sorted")
		}
	}
	if len(problems) > 0 {
		return errors.New("invalid orbit batch: " + strings.Join(problems, "; "))
	}
	return nil
}

func (b OrbitBatch) Filter(minConfidence, minFRP float64, rejectedFlags QualityFlag) []Observation {
	filtered := make([]Observation, 0, len(b.Observations))
	for _, observation := range b.Observations {
		if observation.Confidence >= minConfidence && observation.FRP >= minFRP && observation.Flags&rejectedFlags == 0 {
			filtered = append(filtered, observation)
		}
	}
	return filtered
}
