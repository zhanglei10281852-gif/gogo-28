package incident

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"firescope/geo"
)

// Tracker associates evidence and owns incident lifecycle state.
type Tracker struct {
	mu       sync.RWMutex
	config   Config
	items    map[string]*Incident
	evidence map[string]string
}

func NewTracker(config Config) (*Tracker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Tracker{
		config:   config,
		items:    make(map[string]*Incident),
		evidence: make(map[string]string),
	}, nil
}

func evidenceKey(c Candidate) string { return c.Source + "\x00" + c.EvidenceID }

// Ingest adds evidence to the nearest eligible incident or creates one.
func (t *Tracker) Ingest(candidate Candidate) (Incident, error) {
	candidate = candidate.normalized()
	if err := candidate.Validate(); err != nil {
		return Incident{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	key := evidenceKey(candidate)
	if id, exists := t.evidence[key]; exists {
		item := t.items[id]
		for _, existing := range item.Evidence {
			if evidenceKey(existing) == key {
				if existing != candidate {
					return Incident{}, fmt.Errorf("evidence %q from %q conflicts with prior value", candidate.EvidenceID, candidate.Source)
				}
				return item.clone(), nil
			}
		}
	}

	item, reopened, err := t.closest(candidate)
	if err != nil {
		return Incident{}, err
	}
	if item == nil {
		created := newIncident(candidate)
		t.items[created.ID] = created
		t.evidence[key] = created.ID
		t.maybeActivate(created, candidate.ObservedAt)
		return created.clone(), nil
	}
	previousUpdated := item.UpdatedAt
	item.Evidence = append(item.Evidence, candidate)
	t.evidence[key] = item.ID
	recompute(item)
	if candidate.ObservedAt.After(item.UpdatedAt) {
		item.UpdatedAt = candidate.ObservedAt
	}
	if reopened {
		if err := applyTransition(item, ActiveStatus, candidate.ObservedAt, "new evidence after closure"); err != nil {
			return Incident{}, err
		}
	} else if item.Status == ContainedStatus && candidate.ObservedAt.After(previousUpdated) {
		if err := applyTransition(item, ActiveStatus, candidate.ObservedAt, "new evidence after containment"); err != nil {
			return Incident{}, err
		}
	} else {
		t.maybeActivate(item, candidate.ObservedAt)
	}
	return item.clone(), nil
}

func newIncident(c Candidate) *Incident {
	id := stableIncidentID(c)
	item := &Incident{
		ID:         id,
		Status:     CandidateStatus,
		FirstSeen:  c.ObservedAt,
		LastSeen:   c.ObservedAt,
		UpdatedAt:  c.ObservedAt,
		Centroid:   c.Location,
		Boundary:   geo.BoundingBox{South: c.Location.Lat, West: c.Location.Lon, North: c.Location.Lat, East: c.Location.Lon},
		FRPPeak:    c.FRP,
		Confidence: c.Confidence,
		Evidence:   []Candidate{c},
		Sources:    []string{c.Source},
	}
	return item
}

func stableIncidentID(c Candidate) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%.7f\x00%.7f", c.Source, c.EvidenceID, c.ObservedAt.UnixNano(), c.Location.Lat, c.Location.Lon)
	return fmt.Sprintf("inc-%016x", h.Sum64())
}

func (t *Tracker) closest(c Candidate) (*Incident, bool, error) {
	type match struct {
		item     *Incident
		distance float64
		reopened bool
	}
	var matches []match
	for _, item := range t.items {
		distance, err := geo.Haversine(c.Location, item.Centroid)
		if err != nil {
			return nil, false, err
		}
		if item.Status == ClosedStatus {
			if !c.ObservedAt.After(item.ClosedAt) || c.ObservedAt.After(item.ClosedAt.Add(t.config.ReopenWindow)) || distance > t.config.ReopenDistance {
				continue
			}
			matches = append(matches, match{item: item, distance: distance, reopened: true})
			continue
		}
		if c.ObservedAt.Before(item.FirstSeen.Add(-t.config.AssociationWindow)) || c.ObservedAt.After(item.LastSeen.Add(t.config.AssociationWindow)) {
			continue
		}
		if distance <= t.config.AssociationDistance {
			matches = append(matches, match{item: item, distance: distance})
		}
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].distance != matches[j].distance {
			return matches[i].distance < matches[j].distance
		}
		return matches[i].item.ID < matches[j].item.ID
	})
	return matches[0].item, matches[0].reopened, nil
}

func (t *Tracker) maybeActivate(item *Incident, at time.Time) {
	if item.Status != CandidateStatus {
		return
	}
	if len(item.Evidence) >= t.config.ActivationEvidence && len(item.Sources) >= t.config.ActivationSources {
		if at.Before(item.UpdatedAt) {
			at = item.UpdatedAt
		}
		_ = applyTransition(item, ActiveStatus, at, "activation thresholds met")
	}
}

// Transition applies a strict, explicitly requested lifecycle transition.
func (t *Tracker) Transition(id string, to Status, at time.Time, reason string) (Incident, error) {
	if !to.Valid() {
		return Incident{}, fmt.Errorf("invalid target status %q", to)
	}
	if at.IsZero() {
		return Incident{}, errors.New("transition time is required")
	}
	if strings.TrimSpace(reason) == "" {
		return Incident{}, errors.New("transition reason is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	item, ok := t.items[id]
	if !ok {
		return Incident{}, fmt.Errorf("incident %q not found", id)
	}
	if err := applyTransition(item, to, at.UTC(), reason); err != nil {
		return Incident{}, err
	}
	return item.clone(), nil
}

func applyTransition(item *Incident, to Status, at time.Time, reason string) error {
	if !allowedTransition(item.Status, to) {
		return fmt.Errorf("illegal incident transition %s -> %s", item.Status, to)
	}
	if at.Before(item.UpdatedAt) {
		return fmt.Errorf("transition time %s precedes last update %s", at.Format(time.RFC3339Nano), item.UpdatedAt.Format(time.RFC3339Nano))
	}
	from := item.Status
	item.Status = to
	item.UpdatedAt = at
	if to == ClosedStatus {
		item.ClosedAt = at
	} else if from == ClosedStatus {
		item.ClosedAt = time.Time{}
	}
	item.Transitions = append(item.Transitions, Transition{From: from, To: to, At: at, Reason: strings.TrimSpace(reason)})
	return nil
}

// Advance performs silence and maximum-duration transitions at now.
func (t *Tracker) Advance(now time.Time) ([]Incident, error) {
	if now.IsZero() {
		return nil, errors.New("advance time is required")
	}
	now = now.UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.items))
	for id := range t.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	changed := make([]Incident, 0)
	for _, id := range ids {
		item := t.items[id]
		if now.Before(item.UpdatedAt) {
			return nil, fmt.Errorf("advance time precedes update of incident %q", id)
		}
		var to Status
		var reason string
		silence := now.Sub(item.LastSeen)
		switch {
		case item.Status != ClosedStatus && now.Sub(item.FirstSeen) >= t.config.MaximumDuration:
			to, reason = ClosedStatus, "maximum duration reached"
		case item.Status == CandidateStatus && silence >= t.config.CandidateSilence:
			to, reason = ClosedStatus, "candidate expired without activation"
		case item.Status == ActiveStatus && silence >= t.config.ClosureSilence:
			to, reason = ClosedStatus, "closure silence reached"
		case item.Status == ActiveStatus && silence >= t.config.ContainmentSilence:
			to, reason = ContainedStatus, "containment silence reached"
		case item.Status == ContainedStatus && silence >= t.config.ClosureSilence:
			to, reason = ClosedStatus, "closure silence reached"
		}
		if to != "" {
			if err := applyTransition(item, to, now, reason); err != nil {
				return nil, err
			}
			changed = append(changed, item.clone())
		}
	}
	return changed, nil
}

func (t *Tracker) Get(id string) (Incident, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	item, ok := t.items[id]
	if !ok {
		return Incident{}, false
	}
	return item.clone(), true
}

// List returns snapshots sorted by first observation, then stable ID.
func (t *Tracker) List() []Incident {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]Incident, 0, len(t.items))
	for _, item := range t.items {
		result = append(result, item.clone())
	}
	sort.Slice(result, func(i, j int) bool {
		if cmp := result[i].FirstSeen.Compare(result[j].FirstSeen); cmp != 0 {
			return cmp < 0
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func recompute(item *Incident) {
	sortEvidence(item.Evidence)
	item.FirstSeen = item.Evidence[0].ObservedAt
	item.LastSeen = item.Evidence[len(item.Evidence)-1].ObservedAt
	item.FRPPeak = 0
	item.Confidence = 0
	sourceSet := make(map[string]struct{})
	var x, y, z float64
	for _, evidence := range item.Evidence {
		item.FRPPeak = math.Max(item.FRPPeak, evidence.FRP)
		item.Confidence = math.Max(item.Confidence, evidence.Confidence)
		sourceSet[evidence.Source] = struct{}{}
		lat := evidence.Location.Lat * math.Pi / 180
		lon := evidence.Location.Lon * math.Pi / 180
		x += math.Cos(lat) * math.Cos(lon)
		y += math.Cos(lat) * math.Sin(lon)
		z += math.Sin(lat)
	}
	count := float64(len(item.Evidence))
	x, y, z = x/count, y/count, z/count
	item.Centroid.Lat = math.Atan2(z, math.Sqrt(x*x+y*y)) * 180 / math.Pi
	item.Centroid.Lon = math.Atan2(y, x) * 180 / math.Pi
	item.Boundary = evidenceBounds(item.Evidence)
	item.Sources = item.Sources[:0]
	for source := range sourceSet {
		item.Sources = append(item.Sources, source)
	}
	sort.Strings(item.Sources)
}

// evidenceBounds finds the minimum longitude arc, including dateline-spanning data.
func evidenceBounds(values []Candidate) geo.BoundingBox {
	south, north := values[0].Location.Lat, values[0].Location.Lat
	longitudes := make([]float64, len(values))
	for i, value := range values {
		south = math.Min(south, value.Location.Lat)
		north = math.Max(north, value.Location.Lat)
		lon := value.Location.Lon
		if lon < 0 {
			lon += 360
		}
		longitudes[i] = lon
	}
	sort.Float64s(longitudes)
	largestGap, gapIndex := -1.0, 0
	for i := range longitudes {
		next := longitudes[(i+1)%len(longitudes)]
		if i == len(longitudes)-1 {
			next += 360
		}
		if gap := next - longitudes[i]; gap > largestGap {
			largestGap, gapIndex = gap, i
		}
	}
	west := longitudes[(gapIndex+1)%len(longitudes)]
	east := longitudes[gapIndex]
	if west > 180 {
		west -= 360
	}
	if east > 180 {
		east -= 360
	}
	return geo.BoundingBox{South: south, West: west, North: north, East: east}
}
