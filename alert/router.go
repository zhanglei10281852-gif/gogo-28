package alert

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"firescope/geo"
	"firescope/incident"
)

// Router evaluates rules and maintains channel-scoped suppression state.
type Router struct {
	mu        sync.RWMutex
	rules     []Rule
	routes    map[string]routeState
	envelopes map[string]Envelope
}

func NewRouter(rules []Rule) (*Router, error) {
	normalized := make([]Rule, len(rules))
	seen := make(map[string]bool, len(rules))
	for i, rule := range rules {
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		normalized[i] = normalizeRule(rule)
		if seen[normalized[i].ID] {
			return nil, fmt.Errorf("duplicate rule ID %q", normalized[i].ID)
		}
		seen[normalized[i].ID] = true
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return &Router{rules: normalized, routes: make(map[string]routeState), envelopes: make(map[string]Envelope)}, nil
}

// Route emits one envelope per matching rule channel unless suppressed.
// A severity increase always bypasses an unexpired suppression window.
func (r *Router) Route(item incident.Incident, now time.Time) ([]Envelope, error) {
	if err := item.Validate(); err != nil {
		return nil, fmt.Errorf("incident: %w", err)
	}
	if now.IsZero() {
		return nil, errors.New("route time is required")
	}
	now = now.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Envelope, 0)
	for _, rule := range r.rules {
		matched, err := matches(rule, item)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		if !matched {
			continue
		}
		severity := ruleSeverity(rule, item)
		for _, channel := range rule.Channels {
			key := routeKey(item.ID, rule.ID, channel)
			state, exists := r.routes[key]
			if exists && now.Before(state.LastSent) {
				return nil, fmt.Errorf("route time precedes prior delivery for incident %q rule %q channel %q", item.ID, rule.ID, channel)
			}
			if exists && now.Sub(state.LastSent) < rule.SuppressionFor && severity <= state.Severity {
				continue
			}
			state.Key = key
			state.LastSent = now
			state.Severity = severity
			state.Sequence++
			r.routes[key] = state
			envelope := newEnvelope(item, rule.ID, channel, severity, now, state.Sequence)
			r.envelopes[envelope.ID] = envelope
			result = append(result, envelope)
		}
	}
	sortEnvelopes(result)
	return result, nil
}

func matches(rule Rule, item incident.Incident) (bool, error) {
	if item.FRPPeak < rule.MinFRP || item.Confidence < rule.MinConfidence {
		return false, nil
	}
	statusMatch := false
	for _, status := range rule.Statuses {
		if status == item.Status {
			statusMatch = true
			break
		}
	}
	if !statusMatch {
		return false, nil
	}
	if rule.Region == nil {
		return true, nil
	}
	return rule.Region.Contains(item.Centroid, geo.BoundaryIncluded)
}

func ruleSeverity(rule Rule, item incident.Incident) Severity {
	severity := rule.Severity
	for _, escalation := range rule.Escalations {
		if item.FRPPeak >= escalation.MinFRP && item.Confidence >= escalation.MinConfidence && escalation.Severity > severity {
			severity = escalation.Severity
		}
	}
	return severity
}

func routeKey(incidentID, ruleID, channel string) string {
	return incidentID + "\x00" + ruleID + "\x00" + channel
}

func newEnvelope(item incident.Incident, ruleID, channel string, severity Severity, at time.Time, sequence uint64) Envelope {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%d", item.ID, ruleID, channel, severity, at.UnixNano(), sequence)
	return Envelope{
		ID:             fmt.Sprintf("alert-%016x", h.Sum64()),
		IncidentID:     item.ID,
		RuleID:         ruleID,
		Channel:        channel,
		Severity:       severity,
		IncidentStatus: item.Status,
		Centroid:       item.Centroid,
		FRPPeak:        item.FRPPeak,
		Confidence:     item.Confidence,
		FirstSeen:      item.FirstSeen,
		LastSeen:       item.LastSeen,
		EmittedAt:      at,
	}
}

func sortEnvelopes(values []Envelope) {
	sort.Slice(values, func(i, j int) bool {
		if cmp := values[i].EmittedAt.Compare(values[j].EmittedAt); cmp != 0 {
			return cmp < 0
		}
		if values[i].IncidentID != values[j].IncidentID {
			return values[i].IncidentID < values[j].IncidentID
		}
		if values[i].RuleID != values[j].RuleID {
			return values[i].RuleID < values[j].RuleID
		}
		if values[i].Channel != values[j].Channel {
			return values[i].Channel < values[j].Channel
		}
		return values[i].ID < values[j].ID
	})
}

// Acknowledge marks one delivery acknowledged. Repeating the same acknowledgment is idempotent.
func (r *Router) Acknowledge(id string, at time.Time, by string) (Envelope, error) {
	if at.IsZero() {
		return Envelope{}, errors.New("acknowledgment time is required")
	}
	by = strings.TrimSpace(by)
	if by == "" {
		return Envelope{}, errors.New("acknowledging actor is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	envelope, ok := r.envelopes[id]
	if !ok {
		return Envelope{}, fmt.Errorf("alert %q not found", id)
	}
	at = at.UTC()
	if at.Before(envelope.EmittedAt) {
		return Envelope{}, errors.New("acknowledgment precedes alert emission")
	}
	if !envelope.AcknowledgedAt.IsZero() {
		if envelope.AcknowledgedAt == at && envelope.AcknowledgedBy == by {
			return envelope, nil
		}
		return Envelope{}, fmt.Errorf("alert %q is already acknowledged", id)
	}
	envelope.AcknowledgedAt = at
	envelope.AcknowledgedBy = by
	r.envelopes[id] = envelope
	return envelope, nil
}

// ClearIncident clears all currently active deliveries for an incident.
func (r *Router) ClearIncident(incidentID string, at time.Time, reason string) ([]Envelope, error) {
	incidentID = strings.TrimSpace(incidentID)
	reason = strings.TrimSpace(reason)
	if incidentID == "" || at.IsZero() || reason == "" {
		return nil, errors.New("incident ID, clear time, and reason are required")
	}
	at = at.UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Envelope, 0)
	for id, envelope := range r.envelopes {
		if envelope.IncidentID != incidentID || !envelope.ClearedAt.IsZero() {
			continue
		}
		if at.Before(envelope.EmittedAt) {
			return nil, fmt.Errorf("clear time precedes alert %q emission", id)
		}
		envelope.ClearedAt = at
		envelope.ClearReason = reason
		r.envelopes[id] = envelope
		result = append(result, envelope)
	}
	sortEnvelopes(result)
	return result, nil
}

// List returns every delivery in deterministic order.
func (r *Router) List() []Envelope {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Envelope, 0, len(r.envelopes))
	for _, envelope := range r.envelopes {
		result = append(result, envelope)
	}
	sortEnvelopes(result)
	return result
}

// Snapshot returns map-free, deterministically sorted router state.
func (r *Router) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := Snapshot{Routes: make([]RouteSnapshot, 0, len(r.routes)), Envelopes: make([]Envelope, 0, len(r.envelopes))}
	for _, state := range r.routes {
		snapshot.Routes = append(snapshot.Routes, RouteSnapshot{Key: state.Key, LastSent: state.LastSent, Severity: state.Severity, Sequence: state.Sequence})
	}
	for _, envelope := range r.envelopes {
		snapshot.Envelopes = append(snapshot.Envelopes, envelope)
	}
	sort.Slice(snapshot.Routes, func(i, j int) bool { return snapshot.Routes[i].Key < snapshot.Routes[j].Key })
	sortEnvelopes(snapshot.Envelopes)
	return snapshot
}

// Restore replaces state after validating a snapshot. Rules remain unchanged.
func (r *Router) Restore(snapshot Snapshot) error {
	routes := make(map[string]routeState, len(snapshot.Routes))
	for i, value := range snapshot.Routes {
		if value.Key == "" || value.LastSent.IsZero() || !value.Severity.Valid() || value.Sequence == 0 {
			return fmt.Errorf("invalid route snapshot at index %d", i)
		}
		if _, exists := routes[value.Key]; exists {
			return fmt.Errorf("duplicate route snapshot key %q", value.Key)
		}
		routes[value.Key] = routeState{Key: value.Key, LastSent: value.LastSent.UTC(), Severity: value.Severity, Sequence: value.Sequence}
	}
	envelopes := make(map[string]Envelope, len(snapshot.Envelopes))
	for i, envelope := range snapshot.Envelopes {
		if err := validateEnvelope(envelope); err != nil {
			return fmt.Errorf("envelope snapshot %d: %w", i, err)
		}
		if _, exists := envelopes[envelope.ID]; exists {
			return fmt.Errorf("duplicate envelope ID %q", envelope.ID)
		}
		envelopes[envelope.ID] = envelope
	}
	r.mu.Lock()
	r.routes, r.envelopes = routes, envelopes
	r.mu.Unlock()
	return nil
}

func validateEnvelope(e Envelope) error {
	if e.ID == "" || e.IncidentID == "" || e.RuleID == "" || strings.TrimSpace(e.Channel) == "" {
		return errors.New("identity fields are required")
	}
	if !e.Severity.Valid() || !e.IncidentStatus.Valid() || e.EmittedAt.IsZero() || e.FirstSeen.IsZero() || e.LastSeen.Before(e.FirstSeen) {
		return errors.New("severity, status, or timestamps are invalid")
	}
	if err := e.Centroid.Validate(); err != nil {
		return fmt.Errorf("centroid: %w", err)
	}
	aggregatesValid := finite(e.FRPPeak) && e.FRPPeak >= 0 && finite(e.Confidence) && e.Confidence >= 0 && e.Confidence <= 100
	ackValid := e.AcknowledgedAt.IsZero() == (e.AcknowledgedBy == "") && (e.AcknowledgedAt.IsZero() || !e.AcknowledgedAt.Before(e.EmittedAt))
	clearValid := e.ClearedAt.IsZero() == (e.ClearReason == "") && (e.ClearedAt.IsZero() || !e.ClearedAt.Before(e.EmittedAt))
	if !aggregatesValid || !ackValid || !clearValid {
		return errors.New("aggregate, acknowledgment, or clear fields are invalid")
	}
	return nil
}
