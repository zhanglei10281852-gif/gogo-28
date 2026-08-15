package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"firescope/model"
)

func testSnapshot(t *testing.T, name string) Snapshot {
	t.Helper()
	at := time.Date(2025, 2, 3, 4, 5, 6, 7, time.FixedZone("x", 3600))
	observation := model.Observation{
		ID: "obs-1", Satellite: "sat", Instrument: "cam", AcquiredAt: at,
		Latitude: 10, Longitude: 20, Brightness: 330, BrightnessT31: 290,
		FRP: 12.5, Confidence: 90, DayNight: model.Day, Scan: 1, Track: 1,
	}
	batch, err := model.NewOrbitBatch("orbit-1", "sat", []model.Observation{observation})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshot(name, at, []model.OrbitBatch{batch}, []Tag{{Key: "z", Value: "2"}, {Key: "a", Value: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCanonicalDeterministic(t *testing.T) {
	first := testSnapshot(t, "daily")
	second := testSnapshot(t, "daily")
	first.Metadata.Tags[0], first.Metadata.Tags[1] = first.Metadata.Tags[1], first.Metadata.Tags[0]
	a, err := Canonical(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonical(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical output differs:\n%s\n%s", a, b)
	}
	if first.Metadata.Tags[0].Key != "z" {
		t.Fatal("Canonical mutated caller-owned tags")
	}
}

func TestFileStoreRoundTripListAndCorruption(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested")
	repository, err := NewFileStore(directory, FileOptions{CreateDirectory: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		if err := repository.Save(name, testSnapshot(t, name)); err != nil {
			t.Fatal(err)
		}
	}
	names, err := repository.List()
	if err != nil || !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Fatalf("List() = %v, %v", names, err)
	}
	loaded, err := repository.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata.ObservationCount != 1 || loaded.Metadata.CreatedAt.Location() != time.UTC {
		t.Fatalf("unexpected metadata: %+v", loaded.Metadata)
	}
	path := filepath.Join(directory, "alpha.json")
	data, _ := os.ReadFile(path)
	data = bytes.Replace(data, []byte(`"FRP":12.5`), []byte(`"FRP":13.5`), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load("alpha"); !errors.Is(err, ErrCorruptSnapshot) {
		t.Fatalf("expected corruption error, got %v", err)
	}
}
func TestStrictJSONVersionAndTraversal(t *testing.T) {
	directory := t.TempDir()
	repository, err := NewFileStore(directory, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("valid", testSnapshot(t, "valid")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "valid.json")
	data, _ := os.ReadFile(path)
	unsupported := bytes.Replace(data, []byte(`"version":1`), []byte(`"version":99`), 1)
	if err := os.WriteFile(filepath.Join(directory, "future.json"), bytes.Replace(unsupported, []byte(`"name":"valid"`), []byte(`"name":"future"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load("future"); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected version sentinel, got %v", err)
	}
	unknown := bytes.Replace(data, []byte(`{"version"`), []byte(`{"extra":true,"version"`), 1)
	if err := os.WriteFile(filepath.Join(directory, "unknown.json"), bytes.Replace(unknown, []byte(`"name":"valid"`), []byte(`"name":"unknown"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load("unknown"); !errors.Is(err, ErrCorruptSnapshot) {
		t.Fatalf("expected strict JSON error, got %v", err)
	}
	for _, name := range []string{"../escape", `..\escape`, "/absolute", "bad name"} {
		if err := repository.Save(name, testSnapshot(t, "valid")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Save(%q) error = %v", name, err)
		}
	}
}

func TestMemoryStoreIsolationAndNotFound(t *testing.T) {
	repository := NewMemoryStore()
	snapshot := testSnapshot(t, "memory")
	if err := repository.Save("memory", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Batches[0].Observations[0].FRP = 999
	loaded, err := repository.Load("memory")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Batches[0].Observations[0].FRP != 12.5 {
		t.Fatal("Save retained caller-owned data")
	}
	loaded.Metadata.Tags[0].Value = "changed"
	again, _ := repository.Load("memory")
	if again.Metadata.Tags[0].Value == "changed" {
		t.Fatal("Load returned store-owned data")
	}
	if _, err := repository.Load("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}
