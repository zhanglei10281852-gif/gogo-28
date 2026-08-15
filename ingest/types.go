package ingest

import (
	"errors"
	"fmt"
	"io"

	"firescope/model"
)

type Format string

const (
	FormatAuto Format = "auto"
	FormatJSON Format = "json"
	FormatCSV  Format = "csv"
)

var (
	ErrNilReader   = errors.New("ingest: reader is nil")
	ErrEmptyInput  = errors.New("ingest: input is empty")
	ErrRecordLimit = errors.New("ingest: record limit exceeded")
)

type Options struct {
	Format     Format
	MaxRecords int
}

func (o Options) validate() error {
	switch o.Format {
	case "", FormatAuto, FormatJSON, FormatCSV:
	default:
		return fmt.Errorf("ingest: invalid format %q", o.Format)
	}
	if o.MaxRecords < 0 {
		return errors.New("ingest: MaxRecords must be non-negative")
	}
	return nil
}

type Stats struct{ Records, Accepted, Rejected, Batches int }
type Rejection struct {
	Record, Line int
	Reason       string
}
type Result struct {
	Observations []model.Observation
	Batches      []model.OrbitBatch
	Stats        Stats
	Rejections   []Rejection
}
type Handler func(model.Observation) error
type receiver struct {
	opts       Options
	stats      Stats
	rejections []Rejection
	batches    []model.OrbitBatch
	handler    Handler
}

func (r *receiver) begin(line int) error {
	if r.opts.MaxRecords > 0 && r.stats.Records >= r.opts.MaxRecords {
		return fmt.Errorf("%w: maximum is %d (next record at line %d)", ErrRecordLimit, r.opts.MaxRecords, line)
	}
	r.stats.Records++
	return nil
}
func (r *receiver) reject(line int, err error) {
	r.stats.Rejected++
	r.rejections = append(r.rejections, Rejection{Record: r.stats.Records, Line: line, Reason: err.Error()})
}
func (r *receiver) accept(observation model.Observation) error {
	r.stats.Accepted++
	if r.handler != nil {
		if err := r.handler(observation); err != nil {
			return fmt.Errorf("ingest: handler for record %d: %w", r.stats.Records, err)
		}
	}
	return nil
}
func Ingest(input io.Reader, options Options) (Result, error) {
	var observations []model.Observation
	recv := receiver{opts: options, handler: func(o model.Observation) error {
		observations = append(observations, o)
		return nil
	}}
	err := run(input, &recv)
	model.SortObservations(observations)
	return Result{
		Observations: observations,
		Batches:      recv.batches,
		Stats:        recv.stats,
		Rejections:   recv.rejections,
	}, err
}
func Stream(input io.Reader, options Options, handler Handler) (Stats, []Rejection, error) {
	if handler == nil {
		return Stats{}, nil, errors.New("ingest: handler is nil")
	}
	recv := receiver{opts: options, handler: handler}
	err := run(input, &recv)
	return recv.stats, recv.rejections, err
}
