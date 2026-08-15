package geo

import (
	"errors"
	"fmt"
	"math"
)

const earthRadiusMeters = 6371008.8

type Point struct {
	Lat float64
	Lon float64
}

func (p Point) Validate() error {
	if math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) {
		return errors.New("latitude must be finite")
	}
	if math.IsNaN(p.Lon) || math.IsInf(p.Lon, 0) {
		return errors.New("longitude must be finite")
	}
	if p.Lat < -90 || p.Lat > 90 {
		return fmt.Errorf("latitude %.8g outside [-90,90]", p.Lat)
	}
	if p.Lon < -180 || p.Lon > 180 {
		return fmt.Errorf("longitude %.8g outside [-180,180]", p.Lon)
	}
	return nil
}

func NormalizeLongitude(lon float64) (float64, error) {
	if math.IsNaN(lon) || math.IsInf(lon, 0) {
		return 0, errors.New("longitude must be finite")
	}
	lon = math.Mod(lon+180, 360)
	if lon < 0 {
		lon += 360
	}
	lon -= 180
	if lon == 0 {
		lon = 0
	}
	return lon, nil
}

func NormalizePoint(p Point) (Point, error) {
	if math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) || p.Lat < -90 || p.Lat > 90 {
		return Point{}, fmt.Errorf("invalid latitude %.8g", p.Lat)
	}
	lon, err := NormalizeLongitude(p.Lon)
	if err != nil {
		return Point{}, err
	}
	p.Lon = lon
	if p.Lat == 0 {
		p.Lat = 0
	}
	return p, nil
}

func Haversine(a, b Point) (float64, error) {
	if err := a.Validate(); err != nil {
		return 0, fmt.Errorf("first point: %w", err)
	}
	if err := b.Validate(); err != nil {
		return 0, fmt.Errorf("second point: %w", err)
	}
	lat1, lat2 := degreesToRadians(a.Lat), degreesToRadians(b.Lat)
	dLat := lat2 - lat1
	dLon := degreesToRadians(b.Lon - a.Lon)
	sinLat, sinLon := math.Sin(dLat/2), math.Sin(dLon/2)
	h := sinLat*sinLat + math.Cos(lat1)*math.Cos(lat2)*sinLon*sinLon
	h = math.Min(1, math.Max(0, h))
	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(h)), nil
}

func degreesToRadians(value float64) float64 { return value * math.Pi / 180 }

type BoundingBox struct {
	South float64
	West  float64
	North float64
	East  float64
}

func (b BoundingBox) Validate() error {
	values := []float64{b.South, b.West, b.North, b.East}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("bounding box coordinates must be finite")
		}
	}
	if b.South < -90 || b.North > 90 || b.South > b.North {
		return fmt.Errorf("invalid latitude interval [%g,%g]", b.South, b.North)
	}
	if b.West < -180 || b.West > 180 || b.East < -180 || b.East > 180 {
		return errors.New("bounding box longitudes must be within [-180,180]")
	}
	return nil
}

func (b BoundingBox) CrossesDateline() bool { return b.West > b.East }

func (b BoundingBox) Contains(p Point, rule BoundaryRule) bool {
	if b.Validate() != nil || p.Validate() != nil {
		return false
	}
	insideLat := p.Lat >= b.South && p.Lat <= b.North
	edgeEquivalent := p.Lon == -180 && b.East == 180 || p.Lon == 180 && b.West == -180
	insideLon := p.Lon >= b.West && p.Lon <= b.East || edgeEquivalent
	if b.CrossesDateline() {
		insideLon = p.Lon >= b.West || p.Lon <= b.East
	}
	if !insideLat || !insideLon {
		return false
	}
	if rule == BoundaryIncluded {
		return true
	}
	onLat := p.Lat == b.South || p.Lat == b.North
	onLon := p.Lon == b.West || p.Lon == b.East || edgeEquivalent
	return !onLat && !onLon
}

func (b BoundingBox) Center() (Point, error) {
	if err := b.Validate(); err != nil {
		return Point{}, err
	}
	lon := (b.West + b.East) / 2
	if b.CrossesDateline() {
		lon = (b.West + b.East + 360) / 2
		if lon > 180 {
			lon -= 360
		}
	}
	return Point{Lat: (b.South + b.North) / 2, Lon: lon}, nil
}

func (b BoundingBox) Expand(meters float64) (BoundingBox, error) {
	if err := b.Validate(); err != nil {
		return BoundingBox{}, err
	}
	if math.IsNaN(meters) || math.IsInf(meters, 0) || meters < 0 {
		return BoundingBox{}, errors.New("expansion must be finite and non-negative")
	}
	latDelta := meters / earthRadiusMeters * 180 / math.Pi
	center, _ := b.Center()
	cosLat := math.Cos(degreesToRadians(center.Lat))
	lonDelta := 180.0
	if math.Abs(cosLat) > 1e-12 {
		lonDelta = math.Min(180, latDelta/math.Abs(cosLat))
	}
	west, _ := NormalizeLongitude(b.West - lonDelta)
	east, _ := NormalizeLongitude(b.East + lonDelta)
	if lonDelta >= 180 {
		west, east = -180, 180
	}
	return BoundingBox{South: math.Max(-90, b.South-latDelta), West: west, North: math.Min(90, b.North+latDelta), East: east}, nil
}
