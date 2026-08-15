package incident

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"firescope/geo"
)

// Status is the lifecycle state of a fire incident.
type Status string

const (
	CandidateStatus Status = "candidate"
	ActiveStatus    Status = "active"
	ContainedStatus Status = "contained"
	ClosedStatus    Status = "closed"
)

func (s Status) Valid() bool {
	switch s {
	case CandidateStatus, ActiveStatus, ContainedStatus, ClosedStatus:
		return true
	default:
		return false
	}
}

// Candidate is normalized evidence that can be associated with an incident.
type Candidate struct {
	EvidenceID string
	Source     string
	ObservedAt time.Time
	Location   geo.Point
	FRP        float64
	Confidence float64
}

func (c Candidate) Validate() error {
	var problems []string
	if strings.TrimSpace(c.EvidenceID) == "" {
		problems = append(problems, "evidence ID is required")
	}
	if strings.TrimSpace(c.Source) == "" {
		problems = append(problems, "source is required")
	}
	if c.ObservedAt.IsZero() {
		problems = append(problems, "observation time is required")
	}
	if err := c.Location.Validate(); err != nil {
		problems = append(problems, "location: "+err.Error())
	}
	if !finite(c.FRP) || c.FRP < 0 {
		problems = append(problems, "FRP must be finite and non-negative")
	}
	if !finite(c.Confidence) || c.Confidence < 0 || c.Confidence > 100 {
		problems = append(problems, "confidence must be finite and within [0,100]")
	}
	if len(problems) != 0 {
		return errors.New("invalid candidate: " + strings.Join(problems, "; "))
	}
	return nil
}

func (c Candidate) normalized() Candidate {
	c.EvidenceID = strings.TrimSpace(c.EvidenceID)
	c.Source = strings.TrimSpace(c.Source)
	c.ObservedAt = c.ObservedAt.UTC()
	if c.Location.Lat == 0 {
		c.Location.Lat = 0
	}
	if c.Location.Lon == 0 {
		c.Location.Lon = 0
	}
	return c
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Transition records an immutable state change.
type Transition struct {
	From   Status
	To     Status
	At     time.Time
	Reason string
}

// Incident is a snapshot returned by Tracker. Its slices do not alias tracker state.
type Incident struct {
	ID          string
	Status      Status
	FirstSeen   time.Time
	LastSeen    time.Time
	UpdatedAt   time.Time
	ClosedAt    time.Time
	Centroid    geo.Point
	Boundary    geo.BoundingBox
	FRPPeak     float64
	Confidence  float64
	Evidence    []Candidate
	Sources     []string
	Transitions []Transition
}

func (i Incident) clone() Incident {
	i.Evidence = append([]Candidate(nil), i.Evidence...)
	i.Sources = append([]string(nil), i.Sources...)
	i.Transitions = append([]Transition(nil), i.Transitions...)
	return i
}

func (i Incident) Validate() error {
	if strings.TrimSpace(i.ID) == "" || !i.Status.Valid() || len(i.Evidence) == 0 {
		return errors.New("incident requires an ID, valid status, and evidence")
	}
	if i.FirstSeen.IsZero() || i.LastSeen.Before(i.FirstSeen) || i.UpdatedAt.IsZero() {
		return errors.New("invalid incident timestamps")
	}
	if err := i.Centroid.Validate(); err != nil {
		return fmt.Errorf("centroid: %w", err)
	}
	if err := i.Boundary.Validate(); err != nil {
		return fmt.Errorf("boundary: %w", err)
	}
	if !finite(i.FRPPeak) || i.FRPPeak < 0 || !finite(i.Confidence) || i.Confidence < 0 || i.Confidence > 100 {
		return errors.New("invalid incident aggregate values")
	}
	return nil
}

// Config controls association and lifecycle advancement.
type Config struct {
	AssociationDistance float64
	AssociationWindow   time.Duration
	ActivationEvidence  int
	ActivationSources   int
	CandidateSilence    time.Duration
	ContainmentSilence  time.Duration
	ClosureSilence      time.Duration
	MaximumDuration     time.Duration
	ReopenWindow        time.Duration
	ReopenDistance      float64
}

func DefaultConfig() Config {
	return Config{
		AssociationDistance: 10_000,
		AssociationWindow:   6 * time.Hour,
		ActivationEvidence:  2,
		ActivationSources:   1,
		CandidateSilence:    2 * time.Hour,
		ContainmentSilence:  6 * time.Hour,
		ClosureSilence:      24 * time.Hour,
		MaximumDuration:     14 * 24 * time.Hour,
		ReopenWindow:        48 * time.Hour,
		ReopenDistance:      15_000,
	}
}

func (c Config) Validate() error {
	if !finite(c.AssociationDistance) || c.AssociationDistance <= 0 || !finite(c.ReopenDistance) || c.ReopenDistance <= 0 {
		return errors.New("incident distances must be positive and finite")
	}
	if c.AssociationWindow <= 0 || c.MaximumDuration <= 0 || c.ReopenWindow <= 0 {
		return errors.New("incident windows and maximum duration must be positive")
	}
	if c.ActivationEvidence < 1 || c.ActivationSources < 1 {
		return errors.New("activation evidence and sources must be at least one")
	}
	if c.CandidateSilence <= 0 || c.ContainmentSilence <= 0 || c.ClosureSilence < c.ContainmentSilence {
		return errors.New("silence durations must be positive and closure must not precede containment")
	}
	return nil
}

func sortEvidence(values []Candidate) {
	sort.SliceStable(values, func(a, b int) bool {
		if cmp := values[a].ObservedAt.Compare(values[b].ObservedAt); cmp != 0 {
			return cmp < 0
		}
		if values[a].Source != values[b].Source {
			return values[a].Source < values[b].Source
		}
		return values[a].EvidenceID < values[b].EvidenceID
	})
}

func allowedTransition(from, to Status) bool {
	switch from {
	case CandidateStatus:
		return to == ActiveStatus || to == ClosedStatus
	case ActiveStatus:
		return to == ContainedStatus || to == ClosedStatus
	case ContainedStatus:
		return to == ActiveStatus || to == ClosedStatus
	case ClosedStatus:
		return to == ActiveStatus
	default:
		return false
	}
}
