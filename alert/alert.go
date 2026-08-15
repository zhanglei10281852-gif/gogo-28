package alert

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"firescope/geo"
	"firescope/incident"
)

// Severity is ordered so a larger value is a more urgent alert.
type Severity uint8

const (
	Info Severity = iota + 1
	Warning
	Critical
)

func (s Severity) Valid() bool { return s >= Info && s <= Critical }

// Escalation raises a rule's severity when both optional thresholds are met.
type Escalation struct {
	MinFRP        float64
	MinConfidence float64
	Severity      Severity
}

// Rule selects incidents and fans matching alerts out to every channel.
type Rule struct {
	ID             string
	Region         *geo.Polygon
	MinFRP         float64
	MinConfidence  float64
	Statuses       []incident.Status
	Channels       []string
	Severity       Severity
	Escalations    []Escalation
	SuppressionFor time.Duration
}

func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("invalid alert rule: rule ID is required")
	}
	if !finite(r.MinFRP) || r.MinFRP < 0 || !finite(r.MinConfidence) || r.MinConfidence < 0 || r.MinConfidence > 100 {
		return errors.New("invalid alert rule: thresholds are outside their valid ranges")
	}
	if !r.Severity.Valid() || r.SuppressionFor <= 0 {
		return errors.New("invalid alert rule: severity and suppression window are required")
	}
	if len(r.Statuses) == 0 || len(r.Channels) == 0 {
		return errors.New("invalid alert rule: statuses and channels are required")
	}
	seenStatus := make(map[incident.Status]bool)
	for _, status := range r.Statuses {
		if !status.Valid() || seenStatus[status] {
			return fmt.Errorf("invalid alert rule: status %q is invalid or duplicated", status)
		}
		seenStatus[status] = true
	}
	seenChannel := make(map[string]bool)
	for _, channel := range r.Channels {
		channel = strings.TrimSpace(channel)
		if channel == "" || seenChannel[channel] {
			return fmt.Errorf("invalid alert rule: channel %q is empty or duplicated", channel)
		}
		seenChannel[channel] = true
	}
	if r.Region != nil {
		if err := r.Region.Validate(); err != nil {
			return fmt.Errorf("invalid alert rule region: %w", err)
		}
	}
	for i, escalation := range r.Escalations {
		thresholdsValid := finite(escalation.MinFRP) && escalation.MinFRP >= 0 && finite(escalation.MinConfidence) && escalation.MinConfidence >= 0 && escalation.MinConfidence <= 100
		if !thresholdsValid || !escalation.Severity.Valid() || escalation.Severity <= r.Severity {
			return fmt.Errorf("invalid alert rule: escalation %d has invalid thresholds or severity", i)
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func normalizeRule(r Rule) Rule {
	r.ID = strings.TrimSpace(r.ID)
	r.Statuses = append([]incident.Status(nil), r.Statuses...)
	sort.Slice(r.Statuses, func(i, j int) bool { return r.Statuses[i] < r.Statuses[j] })
	channels := append([]string(nil), r.Channels...)
	for i := range channels {
		channels[i] = strings.TrimSpace(channels[i])
	}
	sort.Strings(channels)
	r.Channels = channels
	r.Escalations = append([]Escalation(nil), r.Escalations...)
	sort.Slice(r.Escalations, func(i, j int) bool {
		if r.Escalations[i].Severity != r.Escalations[j].Severity {
			return r.Escalations[i].Severity < r.Escalations[j].Severity
		}
		return r.Escalations[i].MinFRP < r.Escalations[j].MinFRP
	})
	if r.Region != nil {
		region := geo.Polygon{Outer: append([]geo.Point(nil), r.Region.Outer...), Holes: make([][]geo.Point, len(r.Region.Holes))}
		for i := range r.Region.Holes {
			region.Holes[i] = append([]geo.Point(nil), r.Region.Holes[i]...)
		}
		r.Region = &region
	}
	return r
}

// Envelope is the channel-specific alert delivery and its lifecycle metadata.
type Envelope struct {
	ID             string
	IncidentID     string
	RuleID         string
	Channel        string
	Severity       Severity
	IncidentStatus incident.Status
	Centroid       geo.Point
	FRPPeak        float64
	Confidence     float64
	FirstSeen      time.Time
	LastSeen       time.Time
	EmittedAt      time.Time
	AcknowledgedAt time.Time
	AcknowledgedBy string
	ClearedAt      time.Time
	ClearReason    string
}

func (e Envelope) Active() bool { return e.ClearedAt.IsZero() }

type routeState struct {
	Key        string
	IncidentID string
	RuleID     string
	Channel    string
	LastSent   time.Time
	Severity   Severity
	Sequence   uint64
}

// RouteSnapshot is the serializable form of suppression state.
type RouteSnapshot struct {
	Key        string
	IncidentID string
	RuleID     string
	Channel    string
	LastSent   time.Time
	Severity   Severity
	Sequence   uint64
}

// Snapshot is deterministic and contains all state needed to resume routing.
type Snapshot struct {
	Routes    []RouteSnapshot
	Envelopes []Envelope
}
