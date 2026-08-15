package model

import (
	"math"
	"strings"
	"testing"
	"time"
)

func validObservation(id string, minute int) Observation {
	return Observation{
		ID: id, Satellite: " noaa-20 ", Instrument: "viirs", AcquiredAt: time.Date(2025, 1, 2, 3, minute, 0, 0, time.FixedZone("x", 3600)),
		Latitude: 10, Longitude: 170, Brightness: 330, BrightnessT31: 290, FRP: 12.5,
		Confidence: 80, DayNight: "d", Scan: 0.4, Track: 0.5,
	}
}

func TestObservationNormalizeAndValidate(t *testing.T) {
	o := validObservation(" ", 4)
	if err := o.Normalize(); err != nil {
		t.Fatal(err)
	}
	if o.ID == "" || o.Satellite != "NOAA-20" || o.Instrument != "VIIRS" || o.DayNight != Day {
		t.Fatalf("not normalized: %+v", o)
	}
	if o.AcquiredAt.Location() != time.UTC {
		t.Fatal("time is not UTC")
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationValidationFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Observation)
		want   string
	}{
		{"latitude", func(o *Observation) { o.Latitude = 91 }, "latitude"},
		{"longitude", func(o *Observation) { o.Longitude = math.NaN() }, "longitude"},
		{"brightness", func(o *Observation) { o.Brightness = -1 }, "brightness"},
		{"confidence", func(o *Observation) { o.Confidence = 101 }, "confidence"},
		{"time", func(o *Observation) { o.AcquiredAt = time.Time{} }, "acquisition"},
		{"flag", func(o *Observation) { o.Flags = 1 << 31 }, "quality"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := validObservation("x", 0)
			tc.mutate(&o)
			if err := o.Normalize(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestQualityFlagsAndSort(t *testing.T) {
	flags, err := ParseQualityFlags([]string{"cloud", "WATER", "cloud"})
	if err != nil || !flags.Has(FlagCloud|FlagWater) {
		t.Fatalf("flags=%v err=%v", flags, err)
	}
	if _, err := ParseQualityFlags([]string{"bogus"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
	a, b := validObservation("b", 2), validObservation("a", 1)
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	items := []Observation{a, b}
	SortObservations(items)
	if items[0].ID != "a" {
		t.Fatalf("wrong order: %v", items)
	}
}

func TestOrbitBatch(t *testing.T) {
	a, b := validObservation("b", 2), validObservation("a", 1)
	batch, err := NewOrbitBatch("orbit-1", "noaa-20", []Observation{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Observations[0].ID != "a" || batch.WindowStart.After(batch.WindowEnd) {
		t.Fatalf("bad batch: %+v", batch)
	}
	copy := batch
	copy.Observations = append(copy.Observations, copy.Observations[0])
	if err := copy.Normalize(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
	filtered := batch.Filter(70, 10, FlagCloud)
	if len(filtered) != 2 {
		t.Fatalf("filtered=%v", filtered)
	}
}
