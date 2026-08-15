package model

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"time"
)

type QualityFlag uint32

const (
	FlagCloud QualityFlag = 1 << iota
	FlagWater
	FlagSaturated
	FlagLowSignal
	FlagGeolocationSuspect
	FlagDuplicate
)

const knownQualityFlags = FlagCloud | FlagWater | FlagSaturated | FlagLowSignal | FlagGeolocationSuspect | FlagDuplicate

func (f QualityFlag) Valid() bool { return f&^knownQualityFlags == 0 }

func (f QualityFlag) Has(flag QualityFlag) bool { return flag != 0 && f&flag == flag }

func (f QualityFlag) Names() []string {
	pairs := []struct {
		flag QualityFlag
		name string
	}{
		{FlagCloud, "cloud"}, {FlagWater, "water"}, {FlagSaturated, "saturated"},
		{FlagLowSignal, "low_signal"}, {FlagGeolocationSuspect, "geolocation_suspect"},
		{FlagDuplicate, "duplicate"},
	}
	var names []string
	for _, pair := range pairs {
		if f.Has(pair.flag) {
			names = append(names, pair.name)
		}
	}
	return names
}

func ParseQualityFlags(names []string) (QualityFlag, error) {
	lookup := map[string]QualityFlag{
		"cloud": FlagCloud, "water": FlagWater, "saturated": FlagSaturated,
		"low_signal": FlagLowSignal, "geolocation_suspect": FlagGeolocationSuspect,
		"duplicate": FlagDuplicate,
	}
	var flags QualityFlag
	for i, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		flag, ok := lookup[name]
		if !ok {
			return 0, fmt.Errorf("quality flag %d %q is unknown", i, raw)
		}
		flags |= flag
	}
	return flags, nil
}

type DayNight string

const (
	Day   DayNight = "D"
	Night DayNight = "N"
)

type Observation struct {
	ID            string
	Satellite     string
	Instrument    string
	AcquiredAt    time.Time
	Latitude      float64
	Longitude     float64
	Brightness    float64
	BrightnessT31 float64
	FRP           float64
	Confidence    float64
	DayNight      DayNight
	Scan          float64
	Track         float64
	Flags         QualityFlag
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func cleanZero(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}

func normalizeLongitude(value float64) float64 {
	if value == 180 {
		return -180
	}
	return cleanZero(value)
}

func (o *Observation) Normalize() error {
	if o == nil {
		return errors.New("observation is nil")
	}
	o.ID = strings.TrimSpace(o.ID)
	o.Satellite = strings.ToUpper(strings.TrimSpace(o.Satellite))
	o.Instrument = strings.ToUpper(strings.TrimSpace(o.Instrument))
	o.DayNight = DayNight(strings.ToUpper(strings.TrimSpace(string(o.DayNight))))
	if !o.AcquiredAt.IsZero() {
		o.AcquiredAt = o.AcquiredAt.UTC()
	}
	o.Latitude = cleanZero(o.Latitude)
	o.Longitude = normalizeLongitude(o.Longitude)
	o.Brightness = cleanZero(o.Brightness)
	o.BrightnessT31 = cleanZero(o.BrightnessT31)
	o.FRP = cleanZero(o.FRP)
	o.Confidence = cleanZero(o.Confidence)
	o.Scan = cleanZero(o.Scan)
	o.Track = cleanZero(o.Track)
	if err := o.Validate(); err != nil {
		return err
	}
	if o.ID == "" {
		o.ID = o.StableID()
	}
	return nil
}

func (o Observation) Validate() error {
	var problems []string
	if strings.TrimSpace(o.Satellite) == "" {
		problems = append(problems, "satellite is required")
	}
	if strings.TrimSpace(o.Instrument) == "" {
		problems = append(problems, "instrument is required")
	}
	if o.AcquiredAt.IsZero() {
		problems = append(problems, "acquisition time is required")
	}
	if !finite(o.Latitude) || o.Latitude < -90 || o.Latitude > 90 {
		problems = append(problems, "latitude must be finite and within [-90,90]")
	}
	if !finite(o.Longitude) || o.Longitude < -180 || o.Longitude > 180 {
		problems = append(problems, "longitude must be finite and within [-180,180]")
	}
	if !finite(o.Brightness) || o.Brightness < 0 || o.Brightness > 1000 {
		problems = append(problems, "brightness must be finite and within [0,1000] kelvin")
	}
	if !finite(o.BrightnessT31) || o.BrightnessT31 < 0 || o.BrightnessT31 > 1000 {
		problems = append(problems, "brightness T31 must be finite and within [0,1000] kelvin")
	}
	if !finite(o.FRP) || o.FRP < 0 || o.FRP > 1e7 {
		problems = append(problems, "FRP must be finite and within [0,10000000] MW")
	}
	if !finite(o.Confidence) || o.Confidence < 0 || o.Confidence > 100 {
		problems = append(problems, "confidence must be finite and within [0,100]")
	}
	if o.DayNight != Day && o.DayNight != Night {
		problems = append(problems, "day/night must be D or N")
	}
	if !finite(o.Scan) || o.Scan <= 0 || o.Scan > 20 {
		problems = append(problems, "scan must be finite and within (0,20]")
	}
	if !finite(o.Track) || o.Track <= 0 || o.Track > 20 {
		problems = append(problems, "track must be finite and within (0,20]")
	}
	if !o.Flags.Valid() {
		problems = append(problems, fmt.Sprintf("quality flags contain unknown bits 0x%X", uint32(o.Flags&^knownQualityFlags)))
	}
	if len(problems) > 0 {
		return errors.New("invalid observation: " + strings.Join(problems, "; "))
	}
	return nil
}

func (o Observation) StableID() string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%.7f\x00%.7f", strings.ToUpper(strings.TrimSpace(o.Satellite)), strings.ToUpper(strings.TrimSpace(o.Instrument)), o.AcquiredAt.UTC().UnixNano(), cleanZero(o.Latitude), normalizeLongitude(o.Longitude))
	return fmt.Sprintf("obs-%016x", h.Sum64())
}

func CompareObservations(a, b Observation) int {
	if cmp := a.AcquiredAt.Compare(b.AcquiredAt); cmp != 0 {
		return cmp
	}
	if a.Satellite != b.Satellite {
		if a.Satellite < b.Satellite {
			return -1
		}
		return 1
	}
	if a.Instrument != b.Instrument {
		if a.Instrument < b.Instrument {
			return -1
		}
		return 1
	}
	if a.Latitude != b.Latitude {
		if a.Latitude < b.Latitude {
			return -1
		}
		return 1
	}
	if a.Longitude != b.Longitude {
		if a.Longitude < b.Longitude {
			return -1
		}
		return 1
	}
	if a.ID < b.ID {
		return -1
	}
	if a.ID > b.ID {
		return 1
	}
	return 0
}

func SortObservations(observations []Observation) {
	sort.SliceStable(observations, func(i, j int) bool {
		return CompareObservations(observations[i], observations[j]) < 0
	})
}
