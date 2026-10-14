package oldump

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gzipSuffix = ".gz"

	fileBufferSize = 4 << 20 // read-ahead on the (compressed) file
	lineBufferSize = 1 << 20 // longer lines are reassembled

	// Lines between context and progress checks.
	checkEvery = 4096

	// Channel capacity per worker for lines and results.
	queuePerWorker = 4
)

// Progress is reported periodically while a dump file is read.
type Progress struct {
	Path       string
	ReadBytes  int64 // bytes read from the file on disk (compressed if .gz)
	TotalBytes int64
	Lines      int64
	Kept       int64 // lines that passed the prefilter
}

// StreamOptions configures Stream.
type StreamOptions struct {
	Workers          int
	ProgressInterval time.Duration
	OnProgress       func(Progress)
}

// ErrSkip marks a record the decoder does not want (not fiction, not a candidate...). Decoders wrap
// it to give the reason; Stream counts such lines as skipped rather than failed.
var ErrSkip = errors.New("record skipped")

// Stream reads path, keeps lines accepted by filter (run on the reader goroutine, so it must be
// cheap and only read shared state), decodes them on Workers goroutines and delivers results on
// C in no particular order. Lines for which decode returns an error are dropped and counted: as
// skipped when the error wraps ErrSkip, as failed otherwise.
type Stream[T any] struct {
	C <-chan T

	cancel       context.CancelFunc
	done         chan struct{}
	err          error // written by the reader goroutine, read after done is closed
	skipped      atomic.Int64
	failed       atomic.Int64
	firstFailure atomic.Pointer[error]
}

// countingReader counts the bytes read from the underlying file, for progress reporting.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

// progressReporter tracks Progress and calls onProgress at most every interval.
type progressReporter struct {
	p          Progress
	file       *countingReader
	interval   time.Duration
	onProgress func(Progress)
	last       time.Time
}

// lineReader splits a reader into lines and hands them to fn.
type lineReader struct {
	ctx      context.Context
	path     string
	fn       func(line []byte) (kept bool, err error)
	progress *progressReporter
	long     []byte // reassembles lines longer than the read buffer
}

func (p Progress) Percent() float64 {
	if p.TotalBytes == 0 {
		return 0
	}

	return 100 * float64(p.ReadBytes) / float64(p.TotalBytes)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// ReadLines streams the lines of a dump file (gzip if the name ends in .gz) to fn.
// The slice passed to fn is only valid during the call. Returning an error stops reading.
// onProgress, if non-nil, is called about every interval and once at the end.
func ReadLines(ctx context.Context, path string, interval time.Duration, onProgress func(Progress), fn func(line []byte) (kept bool, err error)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	file := &countingReader{r: f}
	r, closeDecompressor, err := decompress(path, bufio.NewReaderSize(file, fileBufferSize))
	if err != nil {
		return err
	}
	defer closeDecompressor()

	progress := &progressReporter{
		p:    Progress{Path: path, TotalBytes: info.Size()},
		file: file, interval: interval, onProgress: onProgress, last: time.Now(),
	}

	lr := &lineReader{ctx: ctx, path: path, fn: fn, progress: progress}

	return lr.run(bufio.NewReaderSize(r, lineBufferSize))
}

// decompress wraps r in a gzip reader when path ends in .gz. The returned close function is
// always safe to call.
func decompress(path string, r io.Reader) (io.Reader, func(), error) {
	if !strings.HasSuffix(path, gzipSuffix) {
		return r, func() {}, nil
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	return gz, func() { gz.Close() }, nil
}

// run reads lines until EOF, an error from fn, a read error or cancellation.
func (lr *lineReader) run(br *bufio.Reader) error {
	for {
		line, err := lr.next(br)
		if len(line) > 0 {
			if ferr := lr.handle(line); ferr != nil {
				return ferr
			}
		}

		if err == io.EOF {
			lr.progress.report()
			return nil
		}

		if err != nil {
			return fmt.Errorf("%s: %w", lr.path, err)
		}

		if err := lr.checkpoint(); err != nil {
			return err
		}
	}
}

// next returns the next line without its line ending. The slice is valid until the next call.
func (lr *lineReader) next(br *bufio.Reader) ([]byte, error) {
	lr.long = lr.long[:0]
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			lr.long = append(lr.long, chunk...)
			continue
		}

		line := chunk
		if len(lr.long) > 0 {
			lr.long = append(lr.long, chunk...)
			line = lr.long
		}

		return bytes.TrimRight(line, "\r\n"), err
	}
}

// handle passes one line to fn and updates the line counters.
func (lr *lineReader) handle(line []byte) error {
	lr.progress.p.Lines++
	kept, err := lr.fn(line)
	if err != nil {
		return err
	}

	if kept {
		lr.progress.p.Kept++
	}

	return nil
}

// checkpoint periodically checks for cancellation and reports progress.
func (lr *lineReader) checkpoint() error {
	if lr.progress.p.Lines%checkEvery != 0 {
		return nil
	}

	if err := lr.ctx.Err(); err != nil {
		return err
	}

	lr.progress.maybeReport()

	return nil
}

func (pr *progressReporter) report() {
	if pr.onProgress == nil {
		return
	}

	pr.p.ReadBytes = pr.file.n.Load()
	pr.onProgress(pr.p)
}

func (pr *progressReporter) maybeReport() {
	if pr.interval > 0 && time.Since(pr.last) >= pr.interval {
		pr.report()
		pr.last = time.Now()
	}
}

func NewStream[T any](ctx context.Context, path string, opts StreamOptions, filter func(line []byte) bool, decode func(line []byte) (T, error)) *Stream[T] {
	ctx, cancel := context.WithCancel(ctx)
	workers := max(opts.Workers, 1)
	lines := make(chan []byte, queuePerWorker*workers)
	out := make(chan T, queuePerWorker*workers)
	readerDone := make(chan struct{})
	s := &Stream[T]{C: out, cancel: cancel, done: make(chan struct{})}

	go s.read(ctx, path, opts, filter, lines, readerDone)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go s.decodeLines(ctx, &wg, lines, out, decode)
	}

	go s.finish(&wg, out, readerDone)

	return s
}

// read sends the lines accepted by filter to the workers and records the read error.
func (s *Stream[T]) read(ctx context.Context, path string, opts StreamOptions, filter func([]byte) bool, lines chan<- []byte, readerDone chan<- struct{}) {
	defer close(readerDone)
	defer close(lines)

	s.err = ReadLines(ctx, path, opts.ProgressInterval, opts.OnProgress, func(line []byte) (bool, error) {
		if !filter(line) {
			return false, nil
		}

		select {
		case lines <- bytes.Clone(line):
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
}

// decodeLines is one worker: it decodes lines and sends the results until lines is closed.
func (s *Stream[T]) decodeLines(ctx context.Context, wg *sync.WaitGroup, lines <-chan []byte, out chan<- T, decode func([]byte) (T, error)) {
	defer wg.Done()

	for line := range lines {
		v, err := decode(line)
		if err != nil {
			s.drop(err)
			continue
		}

		select {
		case out <- v:
		case <-ctx.Done():
			return
		}
	}
}

// finish closes C once every worker is done, then marks the stream finished.
func (s *Stream[T]) finish(wg *sync.WaitGroup, out chan<- T, readerDone <-chan struct{}) {
	wg.Wait()
	close(out)
	// Workers can exit early on cancellation while the reader is still unwinding.
	<-readerDone
	s.cancel()
	close(s.done)
}

// Err waits for the stream to finish and returns the read error, if any.
// Call it after C is closed.
func (s *Stream[T]) Err() error {
	<-s.done
	return s.err
}

// Stop aborts the stream early and waits for its goroutines to exit.
func (s *Stream[T]) Stop() {
	s.cancel()
	for range s.C {
	}

	<-s.done
}

// drop counts a line whose decoding returned err.
func (s *Stream[T]) drop(err error) {
	if errors.Is(err, ErrSkip) {
		s.skipped.Add(1)
		return
	}

	s.failed.Add(1)
	s.firstFailure.CompareAndSwap(nil, &err)
}

// Skipped is the number of lines that passed the filter but that decode did not want (ErrSkip).
func (s *Stream[T]) Skipped() int64 { return s.skipped.Load() }

// Failed is the number of lines that decode could not read (malformed line or JSON).
func (s *Stream[T]) Failed() int64 { return s.failed.Load() }

// FirstFailure returns the first error counted by Failed, or nil.
func (s *Stream[T]) FirstFailure() error {
	if p := s.firstFailure.Load(); p != nil {
		return *p
	}

	return nil
}
