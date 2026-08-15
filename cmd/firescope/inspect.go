package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"firescope/store"
)

func runInspect(args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		writeInspectHelp(stdout)
		return 0
	}
	set := newFlagSet("inspect")
	directory := set.String("store", "snapshots", "snapshot directory")
	name := set.String("name", "", "snapshot name")
	outputName := set.String("output", "text", "output format")
	if err := set.Parse(args); err != nil {
		return commandError(stderr, "inspect", err, true)
	}
	if set.NArg() != 0 || strings.TrimSpace(*name) == "" {
		return commandError(stderr, "inspect", errors.New("--name is required and positional arguments are not allowed"), true)
	}
	output, err := parseOutput(*outputName)
	if err != nil {
		return commandError(stderr, "inspect", err, true)
	}
	repository, err := store.NewFileStore(*directory, store.FileOptions{})
	if err != nil {
		return commandError(stderr, "inspect", err, false)
	}
	snapshot, err := repository.Load(*name)
	if err != nil {
		return commandError(stderr, "inspect", err, false)
	}
	if err := writeInspected(stdout, output, snapshot); err != nil {
		return commandError(stderr, "inspect", err, false)
	}
	return 0
}

func writeInspectHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: firescope inspect --name NAME [flags]

Loads a snapshot, verifies its schema and checksum, and prints its contents.

Flags:
  --store DIR      snapshot directory (default snapshots)
  --name NAME      snapshot name (required)
  --output FORMAT  text or json (default text)
`)
}

func writeInspected(w io.Writer, output string, snapshot store.Snapshot) error {
	if output == "json" {
		data, err := store.Canonical(snapshot)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	_, err := fmt.Fprintf(w, "Snapshot %s\n  Schema: %s v%d\n  Created: %s\n  Batches: %d\n  Observations: %d\n  Checksum: %s\n",
		snapshot.Metadata.Name, snapshot.Schema, snapshot.Version,
		snapshot.Metadata.CreatedAt.Format(time.RFC3339Nano), len(snapshot.Batches),
		snapshot.Metadata.ObservationCount, snapshot.Checksum)
	return err
}

func runList(args []string, stdout, stderr io.Writer) int {
	if wantsHelp(args) {
		writeListHelp(stdout)
		return 0
	}
	set := newFlagSet("list")
	directory := set.String("store", "snapshots", "snapshot directory")
	outputName := set.String("output", "text", "output format")
	if err := set.Parse(args); err != nil {
		return commandError(stderr, "list", err, true)
	}
	if set.NArg() != 0 {
		return commandError(stderr, "list", errors.New("positional arguments are not allowed"), true)
	}
	output, err := parseOutput(*outputName)
	if err != nil {
		return commandError(stderr, "list", err, true)
	}
	repository, err := store.NewFileStore(*directory, store.FileOptions{})
	if err != nil {
		return commandError(stderr, "list", err, false)
	}
	names, err := repository.List()
	if err != nil {
		return commandError(stderr, "list", err, false)
	}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(names)
	} else {
		for _, name := range names {
			_, err = fmt.Fprintln(stdout, name)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return commandError(stderr, "list", err, false)
	}
	return 0
}

func writeListHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: firescope list [flags]

Lists valid snapshot names in deterministic lexical order.

Flags:
  --store DIR      snapshot directory (default snapshots)
  --output FORMAT  text or json (default text)
`)
}
