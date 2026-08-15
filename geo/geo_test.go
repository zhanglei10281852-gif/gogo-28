package geo

import (
	"math"
	"testing"
)

func TestNormalizeAndDistance(t *testing.T) {
	p, err := NormalizePoint(Point{Lat: 1, Lon: 540})
	if err != nil || p.Lon != -180 {
		t.Fatalf("point=%+v err=%v", p, err)
	}
	if _, err := NormalizePoint(Point{Lat: 91}); err == nil {
		t.Fatal("expected latitude error")
	}
	d, err := Haversine(Point{0, 0}, Point{0, 1})
	if err != nil || math.Abs(d-111195) > 100 {
		t.Fatalf("distance=%f err=%v", d, err)
	}
}

func TestBoundingBoxDateline(t *testing.T) {
	box := BoundingBox{South: -10, North: 10, West: 170, East: -170}
	if err := box.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Point{{0, 175}, {0, -175}, {0, 180}} {
		if !box.Contains(p, BoundaryIncluded) {
			t.Fatalf("expected %+v inside", p)
		}
	}
	if box.Contains(Point{0, 0}, BoundaryIncluded) {
		t.Fatal("zero longitude should be outside")
	}
	if box.Contains(Point{-10, 175}, BoundaryExcluded) {
		t.Fatal("boundary should be excluded")
	}
	world := BoundingBox{South: -90, West: -180, North: 90, East: 180}
	if !world.Contains(Point{0, 42}, BoundaryIncluded) || !world.Contains(Point{0, -180}, BoundaryIncluded) {
		t.Fatal("world box should contain all valid longitudes")
	}
}

func TestGridRoundTripNeighbors(t *testing.T) {
	cell, err := CellForPoint(Point{Lat: 90, Lon: 180}, 3)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := EncodeCell(cell)
	decoded, err := DecodeCell(code)
	if err != nil || decoded != cell {
		t.Fatalf("cell=%+v decoded=%+v err=%v", cell, decoded, err)
	}
	box, err := cell.Bounds()
	if err != nil || !box.Contains(Point{89, -179}, BoundaryIncluded) {
		t.Fatalf("bounds=%+v err=%v", box, err)
	}
	n, err := (Cell{Level: 2, Row: 1, Col: 0}).Neighbors(true)
	if err != nil || len(n) != 8 {
		t.Fatalf("neighbors=%v err=%v", n, err)
	}
}

func TestPolygonBoundaryHoleAndDateline(t *testing.T) {
	polygon, err := NewPolygon(
		[]Point{{-10, 170}, {-10, -170}, {10, -170}, {10, 170}},
		[]Point{{-2, 175}, {-2, -175}, {2, -175}, {2, 175}},
	)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		point Point
		rule  BoundaryRule
		want  bool
	}{
		{Point{5, 179}, BoundaryIncluded, true},
		{Point{0, 179}, BoundaryIncluded, false},
		{Point{-10, 179}, BoundaryIncluded, true},
		{Point{-10, 179}, BoundaryExcluded, false},
		{Point{0, 0}, BoundaryIncluded, false},
	}
	for _, tc := range cases {
		got, err := polygon.Contains(tc.point, tc.rule)
		if err != nil || got != tc.want {
			t.Fatalf("point=%+v got=%v want=%v err=%v", tc.point, got, tc.want, err)
		}
	}
}

func TestGridErrors(t *testing.T) {
	for _, code := range []string{"", "G01-00-0", "G99-0-0", "G01-a-0"} {
		if _, err := DecodeCell(code); err == nil {
			t.Fatalf("accepted %q", code)
		}
	}
}
