package geo

import (
	"errors"
	"fmt"
	"math"
)

type BoundaryRule uint8

const (
	BoundaryExcluded BoundaryRule = iota
	BoundaryIncluded
)

type Polygon struct {
	Outer []Point
	Holes [][]Point
}

func validateRing(name string, ring []Point) error {
	if len(ring) < 3 {
		return fmt.Errorf("%s must contain at least three vertices", name)
	}
	unique := make(map[[2]float64]struct{}, len(ring))
	limit := len(ring)
	if ring[0] == ring[len(ring)-1] {
		limit--
	}
	for i := 0; i < limit; i++ {
		if err := ring[i].Validate(); err != nil {
			return fmt.Errorf("%s vertex %d: %w", name, i, err)
		}
		unique[[2]float64{ring[i].Lat, ring[i].Lon}] = struct{}{}
	}
	if len(unique) < 3 {
		return fmt.Errorf("%s must contain three distinct vertices", name)
	}
	return nil
}

func (p Polygon) Validate() error {
	if err := validateRing("outer ring", p.Outer); err != nil {
		return err
	}
	for i, hole := range p.Holes {
		if err := validateRing(fmt.Sprintf("hole %d", i), hole); err != nil {
			return err
		}
	}
	return nil
}

func unwrapLongitude(lon, reference float64) float64 {
	for lon-reference > 180 {
		lon -= 360
	}
	for lon-reference < -180 {
		lon += 360
	}
	return lon
}

func pointOnSegment(point, a, b Point) bool {
	ax := unwrapLongitude(a.Lon, point.Lon)
	bx := unwrapLongitude(b.Lon, point.Lon)
	px := point.Lon
	cross := (bx-ax)*(point.Lat-a.Lat) - (b.Lat-a.Lat)*(px-ax)
	tolerance := 1e-10 * math.Max(1, math.Abs(bx-ax)+math.Abs(b.Lat-a.Lat))
	if math.Abs(cross) > tolerance {
		return false
	}
	dot := (px-ax)*(px-bx) + (point.Lat-a.Lat)*(point.Lat-b.Lat)
	return dot <= tolerance
}

func ringContains(ring []Point, point Point) (inside, boundary bool) {
	inside = false
	j := len(ring) - 1
	for i := 0; i < len(ring); i++ {
		a, b := ring[j], ring[i]
		if pointOnSegment(point, a, b) {
			return false, true
		}
		ax := unwrapLongitude(a.Lon, point.Lon)
		bx := unwrapLongitude(b.Lon, point.Lon)
		intersects := (a.Lat > point.Lat) != (b.Lat > point.Lat)
		if intersects {
			crossingLon := (bx-ax)*(point.Lat-a.Lat)/(b.Lat-a.Lat) + ax
			if point.Lon < crossingLon {
				inside = !inside
			}
		}
		j = i
	}
	return inside, false
}

func (p Polygon) Contains(point Point, rule BoundaryRule) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	point, err := NormalizePoint(point)
	if err != nil {
		return false, err
	}
	inside, boundary := ringContains(p.Outer, point)
	if boundary {
		return rule == BoundaryIncluded, nil
	}
	if !inside {
		return false, nil
	}
	for _, hole := range p.Holes {
		inHole, onHole := ringContains(hole, point)
		if onHole {
			return rule == BoundaryIncluded, nil
		}
		if inHole {
			return false, nil
		}
	}
	return true, nil
}

func NewPolygon(outer []Point, holes ...[]Point) (Polygon, error) {
	if outer == nil {
		return Polygon{}, errors.New("outer ring is required")
	}
	polygon := Polygon{Outer: append([]Point(nil), outer...), Holes: make([][]Point, len(holes))}
	for i := range holes {
		polygon.Holes[i] = append([]Point(nil), holes[i]...)
	}
	if err := polygon.Validate(); err != nil {
		return Polygon{}, err
	}
	return polygon, nil
}
