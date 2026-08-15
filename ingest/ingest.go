package ingest

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"firescope/model"
)

type jsonObservation struct {
	ID            string   `json:"id"`
	Satellite     string   `json:"satellite"`
	Instrument    string   `json:"instrument"`
	AcquiredAt    string   `json:"acquired_at"`
	Latitude      float64  `json:"latitude"`
	Longitude     float64  `json:"longitude"`
	Brightness    float64  `json:"brightness"`
	BrightnessT31 float64  `json:"brightness_t31"`
	FRP           float64  `json:"frp"`
	Confidence    float64  `json:"confidence"`
	DayNight      string   `json:"day_night"`
	Scan          float64  `json:"scan"`
	Track         float64  `json:"track"`
	QualityFlags  []string `json:"quality_flags"`
}
type jsonBatch struct {
	OrbitID      string            `json:"orbit_id"`
	Satellite    string            `json:"satellite"`
	WindowStart  string            `json:"window_start"`
	WindowEnd    string            `json:"window_end"`
	Observations []json.RawMessage `json:"observations"`
}

var csvColumns = []string{"satellite", "instrument", "acquired_at", "latitude", "longitude", "brightness", "brightness_t31", "frp", "confidence", "day_night", "scan", "track", "quality_flags"}

func run(input io.Reader, recv *receiver) error {
	if input == nil {
		return ErrNilReader
	}
	if err := recv.opts.validate(); err != nil {
		return err
	}
	if recv.opts.Format == "" {
		recv.opts.Format = FormatAuto
	}
	format, first, replay, err := sniff(input, recv.opts.Format)
	if err != nil {
		return err
	}
	switch format {
	case FormatJSON:
		return readJSON(replay, first, recv)
	case FormatCSV:
		return readCSV(replay, recv)
	default:
		return fmt.Errorf("ingest: invalid format %q", format)
	}
}
func DetectFormat(data []byte) (Format, error) {
	format, _, _, err := sniff(bytes.NewReader(data), FormatAuto)
	return format, err
}
func sniff(input io.Reader, requested Format) (Format, byte, io.Reader, error) {
	reader := bufio.NewReader(input)
	var prefix bytes.Buffer
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", 0, nil, ErrEmptyInput
			}
			return "", 0, nil, fmt.Errorf("ingest: read input: %w", err)
		}
		prefix.WriteByte(b)
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			format := requested
			if format == FormatAuto {
				if b == '[' || b == '{' {
					format = FormatJSON
				} else {
					format = FormatCSV
				}
			}
			return format, b, io.MultiReader(bytes.NewReader(prefix.Bytes()), reader), nil
		}
	}
}
func strictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func readJSON(input io.Reader, first byte, recv *receiver) error {
	decoder := json.NewDecoder(input)
	if first == '[' {
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return fmt.Errorf("ingest: JSON top level: %w", err)
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return fmt.Errorf("ingest: JSON record %d: %w", recv.stats.Records+1, err)
			}
			if err := readJSONObservation(raw, recv, 0); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("ingest: JSON array: %w", err)
		}
		return rejectJSONTrailing(decoder)
	}
	if first != '{' {
		return errors.New("ingest: JSON top level must be an array, observation, or orbit batch")
	}
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("ingest: JSON object: %w", err)
	}
	if err := rejectJSONTrailing(decoder); err != nil {
		return err
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return fmt.Errorf("ingest: JSON object: %w", err)
	}
	if _, batch := shape["observations"]; batch {
		return readJSONBatch(raw, recv)
	}
	return readJSONObservation(raw, recv, 1)
}
func rejectJSONTrailing(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("ingest: trailing JSON value")
	}
	return fmt.Errorf("ingest: trailing JSON: %w", err)
}
func readJSONObservation(raw []byte, recv *receiver, line int) error {
	if err := recv.begin(line); err != nil {
		return err
	}
	var wire jsonObservation
	if err := strictJSON(raw, &wire); err != nil {
		recv.reject(line, fmt.Errorf("JSON schema: %w", err))
		return nil
	}
	observation, err := wire.observation()
	if err != nil {
		recv.reject(line, err)
		return nil
	}
	return recv.accept(observation)
}
func (wire jsonObservation) observation() (model.Observation, error) {
	acquiredAt, err := time.Parse(time.RFC3339, wire.AcquiredAt)
	if err != nil {
		return model.Observation{}, fmt.Errorf("acquired_at must be RFC3339: %w", err)
	}
	flags, err := model.ParseQualityFlags(wire.QualityFlags)
	if err != nil {
		return model.Observation{}, err
	}
	observation := model.Observation{
		ID: wire.ID, Satellite: wire.Satellite, Instrument: wire.Instrument,
		AcquiredAt: acquiredAt, Latitude: wire.Latitude, Longitude: wire.Longitude,
		Brightness: wire.Brightness, BrightnessT31: wire.BrightnessT31,
		FRP: wire.FRP, Confidence: wire.Confidence, DayNight: model.DayNight(wire.DayNight),
		Scan: wire.Scan, Track: wire.Track, Flags: flags,
	}
	if err := observation.Normalize(); err != nil {
		return model.Observation{}, err
	}
	return observation, nil
}
func readJSONBatch(raw []byte, recv *receiver) error {
	var wire jsonBatch
	if err := strictJSON(raw, &wire); err != nil {
		return fmt.Errorf("ingest: orbit batch schema: %w", err)
	}
	start, err := optionalTime("window_start", wire.WindowStart)
	if err != nil {
		return err
	}
	end, err := optionalTime("window_end", wire.WindowEnd)
	if err != nil {
		return err
	}
	if start.IsZero() != end.IsZero() {
		return errors.New("ingest: window_start and window_end must both be set")
	}
	if !start.IsZero() && end.Before(start) {
		return errors.New("ingest: window_end precedes window_start")
	}
	batch := model.OrbitBatch{
		OrbitID: wire.OrbitID, Satellite: wire.Satellite,
		WindowStart: start, WindowEnd: end,
		Observations: make([]model.Observation, 0, len(wire.Observations)),
	}
	seen := make(map[string]struct{}, len(wire.Observations))
	batchSatellite := strings.ToUpper(strings.TrimSpace(wire.Satellite))
	for i, rawObservation := range wire.Observations {
		if err := recv.begin(0); err != nil {
			return err
		}
		var item jsonObservation
		if err := strictJSON(rawObservation, &item); err != nil {
			recv.reject(0, fmt.Errorf("batch observation %d schema: %w", i, err))
			continue
		}
		observation, err := item.observation()
		if err != nil {
			recv.reject(0, fmt.Errorf("batch observation %d: %w", i, err))
			continue
		}
		if batchSatellite != "" && observation.Satellite != batchSatellite {
			recv.reject(0, fmt.Errorf("batch observation %d satellite %q differs from batch satellite %q", i, observation.Satellite, batchSatellite))
			continue
		}
		if !start.IsZero() && (observation.AcquiredAt.Before(start) || observation.AcquiredAt.After(end)) {
			recv.reject(0, fmt.Errorf("batch observation %d acquisition time is outside batch window", i))
			continue
		}
		if _, exists := seen[observation.ID]; exists {
			recv.reject(0, fmt.Errorf("batch observation %d duplicates ID %q", i, observation.ID))
			continue
		}
		seen[observation.ID] = struct{}{}
		batch.Observations = append(batch.Observations, observation)
	}
	if err := batch.Normalize(); err != nil {
		return fmt.Errorf("ingest: normalize orbit batch: %w", err)
	}
	recv.stats.Batches++
	recv.batches = append(recv.batches, batch)
	for _, observation := range batch.Observations {
		if err := recv.accept(observation); err != nil {
			return err
		}
	}
	return nil
}
func optionalTime(name, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("ingest: %s must be RFC3339: %w", name, err)
	}
	return parsed, nil
}
func readCSV(input io.Reader, recv *receiver) error {
	reader := csv.NewReader(input)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ErrEmptyInput
		}
		return csvReadError("header", err)
	}
	indexes, err := mapHeader(header, reader)
	if err != nil {
		return err
	}
	reader.FieldsPerRecord = len(header)
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		line := csvErrorLine(readErr)
		if len(record) > 0 {
			line, _ = reader.FieldPos(0)
		}
		if readErr != nil {
			var parseErr *csv.ParseError
			if !errors.As(readErr, &parseErr) || !errors.Is(parseErr.Err, csv.ErrFieldCount) {
				return csvReadError("record", readErr)
			}
			if err := recv.begin(line); err != nil {
				return err
			}
			recv.reject(line, fmt.Errorf("CSV record has %d fields; want %d", len(record), len(header)))
			continue
		}
		if err := recv.begin(line); err != nil {
			return err
		}
		observation, field, err := csvObservation(record, indexes)
		if err != nil {
			if field >= 0 && field < len(record) {
				line, _ = reader.FieldPos(field)
			}
			recv.reject(line, err)
			continue
		}
		if err := recv.accept(observation); err != nil {
			return err
		}
	}
}
func mapHeader(header []string, reader *csv.Reader) (map[string]int, error) {
	allowed := make(map[string]bool, len(csvColumns)+1)
	allowed["id"] = true
	for _, name := range csvColumns {
		allowed[name] = true
	}
	indexes := make(map[string]int, len(header))
	for i, raw := range header {
		name := strings.TrimSpace(raw)
		line, _ := reader.FieldPos(i)
		if name == "" {
			return nil, fmt.Errorf("ingest: CSV line %d has an empty header", line)
		}
		if !allowed[name] {
			return nil, fmt.Errorf("ingest: CSV line %d has unknown header %q", line, raw)
		}
		if previous, exists := indexes[name]; exists {
			return nil, fmt.Errorf("ingest: CSV line %d has duplicate header %q at columns %d and %d", line, name, previous+1, i+1)
		}
		indexes[name] = i
	}
	var missing []string
	for _, name := range csvColumns {
		if _, exists := indexes[name]; !exists {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("ingest: CSV header missing required columns: %s", strings.Join(missing, ", "))
	}
	return indexes, nil
}
func csvObservation(record []string, indexes map[string]int) (model.Observation, int, error) {
	value := func(name string) string { return record[indexes[name]] }
	acquiredAt, err := time.Parse(time.RFC3339, value("acquired_at"))
	if err != nil {
		return model.Observation{}, indexes["acquired_at"], fmt.Errorf("acquired_at must be RFC3339: %w", err)
	}
	flags, err := parseCSVFlags(value("quality_flags"))
	if err != nil {
		return model.Observation{}, indexes["quality_flags"], err
	}
	names := []string{"latitude", "longitude", "brightness", "brightness_t31", "frp", "confidence", "scan", "track"}
	numbers := make(map[string]float64, len(names))
	for _, name := range names {
		index := indexes[name]
		numbers[name], err = strconv.ParseFloat(strings.TrimSpace(record[index]), 64)
		if err != nil {
			return model.Observation{}, index, fmt.Errorf("%s must be a number: %w", name, err)
		}
	}
	observation := model.Observation{
		Satellite: value("satellite"), Instrument: value("instrument"), AcquiredAt: acquiredAt,
		Latitude: numbers["latitude"], Longitude: numbers["longitude"], Brightness: numbers["brightness"],
		BrightnessT31: numbers["brightness_t31"], FRP: numbers["frp"], Confidence: numbers["confidence"],
		DayNight: model.DayNight(value("day_night")), Scan: numbers["scan"], Track: numbers["track"], Flags: flags,
	}
	if index, exists := indexes["id"]; exists {
		observation.ID = record[index]
	}
	if err := observation.Normalize(); err != nil {
		return model.Observation{}, -1, err
	}
	return observation, -1, nil
}
func parseCSVFlags(value string) (model.QualityFlag, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parts := strings.Split(value, "|")
	return model.ParseQualityFlags(parts)
}
func csvErrorLine(err error) int {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		if parseErr.StartLine > 0 {
			return parseErr.StartLine
		}
		return parseErr.Line
	}
	return 0
}
func csvReadError(part string, err error) error {
	line := csvErrorLine(err)
	if line > 0 {
		return fmt.Errorf("ingest: CSV %s at physical line %d: %w", part, line, err)
	}
	return fmt.Errorf("ingest: CSV %s: %w", part, err)
}
