package geo

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	MinGridLevel = 1
	MaxGridLevel = 20
)

type Cell struct {
	Level int
	Row   int
	Col   int
}

func gridSize(level int) (rows, cols int, err error) {
	if level < MinGridLevel || level > MaxGridLevel {
		return 0, 0, fmt.Errorf("grid level %d outside [%d,%d]", level, MinGridLevel, MaxGridLevel)
	}
	rows = 1 << level
	cols = 1 << (level + 1)
	return rows, cols, nil
}

func (c Cell) Validate() error {
	rows, cols, err := gridSize(c.Level)
	if err != nil {
		return err
	}
	if c.Row < 0 || c.Row >= rows {
		return fmt.Errorf("grid row %d outside [0,%d)", c.Row, rows)
	}
	if c.Col < 0 || c.Col >= cols {
		return fmt.Errorf("grid column %d outside [0,%d)", c.Col, cols)
	}
	return nil
}

func EncodeCell(c Cell) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf("G%02d-%X-%X", c.Level, c.Row, c.Col), nil
}

func DecodeCell(code string) (Cell, error) {
	parts := strings.Split(code, "-")
	if len(parts) != 3 || len(parts[0]) != 3 || parts[0][0] != 'G' {
		return Cell{}, fmt.Errorf("invalid grid code %q", code)
	}
	level, err := strconv.Atoi(parts[0][1:])
	if err != nil {
		return Cell{}, fmt.Errorf("invalid grid level: %w", err)
	}
	row, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return Cell{}, fmt.Errorf("invalid grid row: %w", err)
	}
	col, err := strconv.ParseInt(parts[2], 16, 32)
	if err != nil {
		return Cell{}, fmt.Errorf("invalid grid column: %w", err)
	}
	cell := Cell{Level: level, Row: int(row), Col: int(col)}
	if err := cell.Validate(); err != nil {
		return Cell{}, err
	}
	canonical, _ := EncodeCell(cell)
	if code != canonical {
		return Cell{}, fmt.Errorf("non-canonical grid code %q; expected %q", code, canonical)
	}
	return cell, nil
}

func CellForPoint(point Point, level int) (Cell, error) {
	point, err := NormalizePoint(point)
	if err != nil {
		return Cell{}, err
	}
	rows, cols, err := gridSize(level)
	if err != nil {
		return Cell{}, err
	}
	row := int(math.Floor((point.Lat + 90) / 180 * float64(rows)))
	col := int(math.Floor((point.Lon + 180) / 360 * float64(cols)))
	if row == rows {
		row--
	}
	if col == cols {
		col = 0
	}
	return Cell{Level: level, Row: row, Col: col}, nil
}

func (c Cell) Bounds() (BoundingBox, error) {
	rows, cols, err := gridSize(c.Level)
	if err != nil {
		return BoundingBox{}, err
	}
	if err := c.Validate(); err != nil {
		return BoundingBox{}, err
	}
	latStep := 180 / float64(rows)
	lonStep := 360 / float64(cols)
	return BoundingBox{
		South: -90 + float64(c.Row)*latStep,
		West:  -180 + float64(c.Col)*lonStep,
		North: -90 + float64(c.Row+1)*latStep,
		East:  -180 + float64(c.Col+1)*lonStep,
	}, nil
}

func (c Cell) Center() (Point, error) {
	bounds, err := c.Bounds()
	if err != nil {
		return Point{}, err
	}
	return bounds.Center()
}

func (c Cell) Neighbors(diagonal bool) ([]Cell, error) {
	rows, cols, err := gridSize(c.Level)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	offsets := [][2]int{{-1, 0}, {0, -1}, {0, 1}, {1, 0}}
	if diagonal {
		offsets = append(offsets, [2]int{-1, -1}, [2]int{-1, 1}, [2]int{1, -1}, [2]int{1, 1})
	}
	neighbors := make([]Cell, 0, len(offsets))
	for _, offset := range offsets {
		row := c.Row + offset[0]
		if row < 0 || row >= rows {
			continue
		}
		col := (c.Col + offset[1] + cols) % cols
		neighbors = append(neighbors, Cell{Level: c.Level, Row: row, Col: col})
	}
	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].Row != neighbors[j].Row {
			return neighbors[i].Row < neighbors[j].Row
		}
		return neighbors[i].Col < neighbors[j].Col
	})
	return neighbors, nil
}
