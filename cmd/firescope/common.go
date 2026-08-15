package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"firescope/ingest"
)

func newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func parseFormat(raw string) (ingest.Format, error) {
	format := ingest.Format(strings.ToLower(strings.TrimSpace(raw)))
	switch format {
	case ingest.FormatAuto, ingest.FormatJSON, ingest.FormatCSV:
		return format, nil
	default:
		return "", fmt.Errorf("invalid format %q (want auto, json, or csv)", raw)
	}
}

func parseOutput(raw string) (string, error) {
	output := strings.ToLower(strings.TrimSpace(raw))
	if output != "text" && output != "json" {
		return "", fmt.Errorf("invalid output %q (want text or json)", raw)
	}
	return output, nil
}

func parseNow(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("--now must be RFC3339: %w", err)
	}
	return value.UTC(), nil
}

func openInput(path string, stdin io.Reader) (io.Reader, func() error, error) {
	if path == "-" {
		if stdin == nil {
			return nil, nil, errors.New("stdin is nil")
		}
		return stdin, func() error { return nil }, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input %q: %w", path, err)
	}
	return file, file.Close, nil
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}
