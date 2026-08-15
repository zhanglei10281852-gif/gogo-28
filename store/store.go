// Package store persists deterministic, versioned FireScope snapshots.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"firescope/model"
)

const (
	CurrentVersion = 1
	SchemaName     = "firescope.snapshot"
	fileExtension  = ".json"
)

var (
	ErrNotFound           = errors.New("snapshot not found")
	ErrInvalidName        = errors.New("invalid snapshot name")
	ErrUnsupportedVersion = errors.New("unsupported snapshot version")
	ErrCorruptSnapshot    = errors.New("corrupt snapshot")
)

// Tag is a metadata item. A slice, rather than a map, makes ordering explicit.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Metadata describes snapshot provenance and contents.
type Metadata struct {
	Name             string    `json:"name"`
	CreatedAt        time.Time `json:"created_at"`
	ObservationCount int       `json:"observation_count"`
	Tags             []Tag     `json:"tags"`
}

// Snapshot is the stable public representation of persisted data.
type Snapshot struct {
	Version  int                `json:"version"`
	Schema   string             `json:"schema"`
	Metadata Metadata           `json:"metadata"`
	Batches  []model.OrbitBatch `json:"batches"`
	Checksum string             `json:"checksum"`
}

// Store is implemented by both disk and memory repositories.
type Store interface {
	Save(name string, snapshot Snapshot) error
	Load(name string) (Snapshot, error)
	List() ([]string, error)
}

// NewSnapshot normalizes and checks a snapshot before returning it.
func NewSnapshot(name string, createdAt time.Time, batches []model.OrbitBatch, tags []Tag) (Snapshot, error) {
	s := Snapshot{
		Version:  CurrentVersion,
		Schema:   SchemaName,
		Metadata: Metadata{Name: name, CreatedAt: createdAt, Tags: tags},
		Batches:  batches,
	}
	if err := prepare(&s, name); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Canonical returns the exact deterministic JSON representation used by stores.
func Canonical(snapshot Snapshot) ([]byte, error) {
	if err := prepare(&snapshot, snapshot.Metadata.Name); err != nil {
		return nil, err
	}
	return marshalCanonical(snapshot)
}

func prepare(snapshot *Snapshot, name string) error {
	if snapshot == nil {
		return errors.New("snapshot is nil")
	}
	detach(snapshot)
	if err := validateName(name); err != nil {
		return err
	}
	if snapshot.Version == 0 {
		snapshot.Version = CurrentVersion
	}
	if snapshot.Version != CurrentVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, snapshot.Version)
	}
	if snapshot.Schema == "" {
		snapshot.Schema = SchemaName
	}
	if snapshot.Schema != SchemaName {
		return fmt.Errorf("%w: schema %q", ErrCorruptSnapshot, snapshot.Schema)
	}
	if snapshot.Metadata.Name == "" {
		snapshot.Metadata.Name = name
	}
	if snapshot.Metadata.Name != name {
		return fmt.Errorf("%w: metadata name %q does not match %q", ErrCorruptSnapshot, snapshot.Metadata.Name, name)
	}
	if snapshot.Metadata.CreatedAt.IsZero() {
		return errors.New("snapshot creation time is required")
	}
	snapshot.Metadata.CreatedAt = snapshot.Metadata.CreatedAt.UTC()
	if snapshot.Batches == nil {
		snapshot.Batches = []model.OrbitBatch{}
	}
	if snapshot.Metadata.Tags == nil {
		snapshot.Metadata.Tags = []Tag{}
	}
	if err := normalizeTags(snapshot.Metadata.Tags); err != nil {
		return err
	}
	count := 0
	seen := make(map[string]struct{}, len(snapshot.Batches))
	for i := range snapshot.Batches {
		if err := snapshot.Batches[i].Normalize(); err != nil {
			return fmt.Errorf("batch %d: %w", i, err)
		}
		id := snapshot.Batches[i].OrbitID
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate orbit ID %q", id)
		}
		seen[id] = struct{}{}
		count += len(snapshot.Batches[i].Observations)
	}
	sort.SliceStable(snapshot.Batches, func(i, j int) bool {
		if snapshot.Batches[i].WindowStart.Equal(snapshot.Batches[j].WindowStart) {
			return snapshot.Batches[i].OrbitID < snapshot.Batches[j].OrbitID
		}
		return snapshot.Batches[i].WindowStart.Before(snapshot.Batches[j].WindowStart)
	})
	snapshot.Metadata.ObservationCount = count
	digest, err := contentDigest(*snapshot)
	if err != nil {
		return err
	}
	snapshot.Checksum = digest
	return nil
}

func normalizeTags(tags []Tag) error {
	for i := range tags {
		tags[i].Key = strings.TrimSpace(tags[i].Key)
		tags[i].Value = strings.TrimSpace(tags[i].Value)
		if tags[i].Key == "" {
			return fmt.Errorf("tag %d has an empty key", i)
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Key == tags[j].Key {
			return tags[i].Value < tags[j].Value
		}
		return tags[i].Key < tags[j].Key
	})
	for i := 1; i < len(tags); i++ {
		if tags[i-1].Key == tags[i].Key {
			return fmt.Errorf("duplicate tag key %q", tags[i].Key)
		}
	}
	return nil
}

func contentDigest(snapshot Snapshot) (string, error) {
	snapshot.Checksum = ""
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func marshalCanonical(snapshot Snapshot) ([]byte, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func decodeSnapshot(data []byte, requestedName string) (Snapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: invalid JSON: %v", ErrCorruptSnapshot, err)
	}
	if err := requireEOF(decoder); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorruptSnapshot, err)
	}
	if snapshot.Version != CurrentVersion {
		return Snapshot{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, snapshot.Version)
	}
	if snapshot.Schema != SchemaName {
		return Snapshot{}, fmt.Errorf("%w: schema %q", ErrCorruptSnapshot, snapshot.Schema)
	}
	storedChecksum := snapshot.Checksum
	if len(storedChecksum) != sha256.Size*2 {
		return Snapshot{}, fmt.Errorf("%w: malformed checksum", ErrCorruptSnapshot)
	}
	if _, err := hex.DecodeString(storedChecksum); err != nil {
		return Snapshot{}, fmt.Errorf("%w: malformed checksum", ErrCorruptSnapshot)
	}
	digest, err := contentDigest(snapshot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: checksum: %v", ErrCorruptSnapshot, err)
	}
	if !equalDigest(storedChecksum, digest) {
		return Snapshot{}, fmt.Errorf("%w: checksum mismatch", ErrCorruptSnapshot)
	}
	if err := prepare(&snapshot, requestedName); err != nil {
		if errors.Is(err, ErrUnsupportedVersion) || errors.Is(err, ErrCorruptSnapshot) {
			return Snapshot{}, err
		}
		return Snapshot{}, fmt.Errorf("%w: %v", ErrCorruptSnapshot, err)
	}
	if snapshot.Checksum != storedChecksum {
		return Snapshot{}, fmt.Errorf("%w: non-canonical content", ErrCorruptSnapshot)
	}
	return snapshot, nil
}

func equalDigest(a, b string) bool {
	left, leftErr := hex.DecodeString(a)
	right, rightErr := hex.DecodeString(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("%w: %q", ErrInvalidName, name)
		}
	}
	return nil
}

// FileOptions controls directory and file creation. Zero modes use 0750/0640.
type FileOptions struct {
	CreateDirectory bool
	DirectoryMode   fs.FileMode
	FileMode        fs.FileMode
}

// FileStore stores each named snapshot in a separate JSON file.
type FileStore struct {
	directory string
	fileMode  fs.FileMode
}

func NewFileStore(directory string, options FileOptions) (*FileStore, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("store directory is required")
	}
	dirMode := options.DirectoryMode
	if dirMode == 0 {
		dirMode = 0o750
	}
	fileMode := options.FileMode
	if fileMode == 0 {
		fileMode = 0o640
	}
	if options.CreateDirectory {
		if err := os.MkdirAll(directory, dirMode); err != nil {
			return nil, fmt.Errorf("create store directory: %w", err)
		}
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("open store directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("store path is not a directory")
	}
	return &FileStore{directory: directory, fileMode: fileMode}, nil
}

func (s *FileStore) path(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.directory, name+fileExtension), nil
}

func (s *FileStore) Save(name string, snapshot Snapshot) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := prepare(&snapshot, name); err != nil {
		return err
	}
	data, err := marshalCanonical(snapshot)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.directory, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			temporary.Close()
			os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(s.fileMode); err != nil {
		return fmt.Errorf("set snapshot mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	committed = true
	return nil
}

func (s *FileStore) Load(name string) (Snapshot, error) {
	path, err := s.path(name)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}
	return decodeSnapshot(data, name)
}

func (s *FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != fileExtension {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), fileExtension)
		if validateName(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// MemoryStore is a concurrent, copy-isolating in-memory Store.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string][]byte)}
}

func (s *MemoryStore) Save(name string, snapshot Snapshot) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := prepare(&snapshot, name); err != nil {
		return err
	}
	data, err := marshalCanonical(snapshot)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.data[name] = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Load(name string) (Snapshot, error) {
	if err := validateName(name); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	data, exists := s.data[name]
	data = append([]byte(nil), data...)
	s.mu.RUnlock()
	if !exists {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return decodeSnapshot(data, name)
}

func (s *MemoryStore) List() ([]string, error) {
	s.mu.RLock()
	names := make([]string, 0, len(s.data))
	for name := range s.data {
		names = append(names, name)
	}
	s.mu.RUnlock()
	sort.Strings(names)
	return names, nil
}

// detach gives normalization private ownership of every mutable slice.
func detach(snapshot *Snapshot) {
	snapshot.Metadata.Tags = append([]Tag(nil), snapshot.Metadata.Tags...)
	snapshot.Batches = append([]model.OrbitBatch(nil), snapshot.Batches...)
	for i := range snapshot.Batches {
		snapshot.Batches[i].Observations = append([]model.Observation(nil), snapshot.Batches[i].Observations...)
	}
}
