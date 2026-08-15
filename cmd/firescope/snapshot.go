package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"firescope/ingest"
	"firescope/model"
	"firescope/store"
)

type snapshotReceipt struct {
	Name             string    `json:"name"`
	CreatedAt        time.Time `json:"created_at"`
	ObservationCount int       `json:"observation_count"`
	BatchCount       int       `json:"batch_count"`
	Checksum         string    `json:"checksum"`
}

func runSnapshot(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		writeSnapshotHelp(stdout)
		return 0
	}
	set := newFlagSet("snapshot")
	inputPath := set.String("input", "-", "input path or - for stdin")
	formatName := set.String("format", "auto", "input format")
	storePath := set.String("store", "snapshots", "snapshot directory")
	name := set.String("name", "", "snapshot name")
	orbitID := set.String("orbit-id", "", "orbit ID for flat observation input")
	nowText := set.String("now", "", "RFC3339 creation time")
	outputName := set.String("output", "text", "output format")
	var tags stringList
	set.Var(&tags, "tag", "metadata key=value (repeatable)")
	if err := set.Parse(args); err != nil {
		return commandError(stderr, "snapshot", err, true)
	}
	if set.NArg() != 0 || strings.TrimSpace(*name) == "" {
		return commandError(stderr, "snapshot", errors.New("--name is required and positional arguments are not allowed"), true)
	}
	format, err := parseFormat(*formatName)
	if err != nil {
		return commandError(stderr, "snapshot", err, true)
	}
	output, err := parseOutput(*outputName)
	if err != nil {
		return commandError(stderr, "snapshot", err, true)
	}
	now, err := parseNow(*nowText)
	if err != nil {
		return commandError(stderr, "snapshot", err, true)
	}
	receipt, err := createSnapshot(*inputPath, format, *storePath, *name, *orbitID, now, tags, stdin)
	if err != nil {
		return commandError(stderr, "snapshot", err, false)
	}
	if err := writeSnapshotReceipt(stdout, output, receipt); err != nil {
		return commandError(stderr, "snapshot", err, false)
	}
	return 0
}

func writeSnapshotHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: firescope snapshot --name NAME [flags]

Strictly ingests an orbit batch and atomically replaces STORE/NAME.json.
Flat JSON arrays or CSV require --orbit-id to construct one batch.

Flags:
  --input PATH       input file, or - for stdin (default -)
  --format FORMAT    auto, json, or csv (default auto)
  --store DIR        snapshot directory (default snapshots)
  --name NAME        safe snapshot name (required)
  --orbit-id ID      orbit ID for flat observations
  --now TIME         RFC3339 creation time (default latest batch time)
  --tag KEY=VALUE    metadata tag; repeatable
  --output FORMAT    text or json (default text)
`)
}

func createSnapshot(inputPath string, format ingest.Format, directory, name, orbitID string, now time.Time, rawTags []string, stdin io.Reader) (snapshotReceipt, error) {
	reader, closeInput, err := openInput(inputPath, stdin)
	if err != nil {
		return snapshotReceipt{}, err
	}
	defer closeInput()
	loaded, err := ingest.Ingest(reader, ingest.Options{Format: format})
	if err != nil {
		return snapshotReceipt{}, err
	}
	if loaded.Stats.Rejected != 0 {
		return snapshotReceipt{}, fmt.Errorf("snapshot input rejected %d record(s)", loaded.Stats.Rejected)
	}
	batches := loaded.Batches
	if len(batches) == 0 {
		if strings.TrimSpace(orbitID) == "" {
			return snapshotReceipt{}, errors.New("input has no orbit batch; --orbit-id is required for flat observations")
		}
		if len(loaded.Observations) == 0 {
			return snapshotReceipt{}, errors.New("input has no observations")
		}
		batch, err := model.NewOrbitBatch(orbitID, loaded.Observations[0].Satellite, loaded.Observations)
		if err != nil {
			return snapshotReceipt{}, fmt.Errorf("construct orbit batch: %w", err)
		}
		batches = []model.OrbitBatch{batch}
	} else if strings.TrimSpace(orbitID) != "" {
		return snapshotReceipt{}, errors.New("--orbit-id cannot be used when input already contains an orbit batch")
	}
	latest := batches[0].WindowEnd
	for _, batch := range batches[1:] {
		if batch.WindowEnd.After(latest) {
			latest = batch.WindowEnd
		}
	}
	if now.IsZero() {
		now = latest
	}
	if now.Before(latest) {
		return snapshotReceipt{}, fmt.Errorf("--now %s precedes latest batch observation %s", now.Format(time.RFC3339Nano), latest.Format(time.RFC3339Nano))
	}
	tags, err := parseTags(rawTags)
	if err != nil {
		return snapshotReceipt{}, err
	}
	snapshot, err := store.NewSnapshot(name, now, batches, tags)
	if err != nil {
		return snapshotReceipt{}, err
	}
	repository, err := store.NewFileStore(directory, store.FileOptions{CreateDirectory: true})
	if err != nil {
		return snapshotReceipt{}, err
	}
	if err := repository.Save(name, snapshot); err != nil {
		return snapshotReceipt{}, err
	}
	return snapshotReceipt{Name: name, CreatedAt: snapshot.Metadata.CreatedAt,
		ObservationCount: snapshot.Metadata.ObservationCount, BatchCount: len(snapshot.Batches), Checksum: snapshot.Checksum}, nil
}

func parseTags(values []string) ([]store.Tag, error) {
	tags := make([]store.Tag, 0, len(values))
	for _, value := range values {
		key, text, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --tag %q (want KEY=VALUE)", value)
		}
		tags = append(tags, store.Tag{Key: key, Value: text})
	}
	return tags, nil
}

func writeSnapshotReceipt(w io.Writer, output string, receipt snapshotReceipt) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(receipt)
	}
	_, err := fmt.Fprintf(w, "Snapshot %s\n  Created: %s\n  Batches: %d\n  Observations: %d\n  Checksum: %s\n",
		receipt.Name, receipt.CreatedAt.Format(time.RFC3339Nano), receipt.BatchCount, receipt.ObservationCount, receipt.Checksum)
	return err
}
