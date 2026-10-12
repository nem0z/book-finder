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

// Progress is reported periodically while a dump file is read.
type Progress struct {
	Path       string
	ReadBytes  int64 // bytes read from the file on disk (compressed if .gz)
	TotalBytes int64
	Lines      int64
	Kept       int64 // lines that passed the prefilter
}

func (p Progress) Percent() float64 {
	if p.TotalBytes == 0 {
		return 0
	}
	return 100 * float64(p.ReadBytes) / float64(p.TotalBytes)
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
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

	cr := &countingReader{r: f}
	var r io.Reader = bufio.NewReaderSize(cr, 4<<20)
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}

	p := Progress{Path: path, TotalBytes: info.Size()}
	report := func() {
		if onProgress != nil {
			p.ReadBytes = cr.n.Load()
			onProgress(p)
		}
	}
	last := time.Now()

	br := bufio.NewReaderSize(r, 1<<20)
	var long []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		line := chunk
		if len(long) > 0 {
			long = append(long, chunk...)
			line = long
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			p.Lines++
			kept, ferr := fn(line)
			if ferr != nil {
				return ferr
			}
			if kept {
				p.Kept++
			}
		}
		long = long[:0]

		if err == io.EOF {
			report()
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if p.Lines%4096 == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if interval > 0 && time.Since(last) >= interval {
				report()
				last = time.Now()
			}
		}
	}
}

// StreamOptions configures Stream.
type StreamOptions struct {
	Workers          int
	ProgressInterval time.Duration
	OnProgress       func(Progress)
}

// Stream reads path, keeps lines accepted by filter (run on the reader goroutine, so it must be
// cheap and only read shared state), decodes them on Workers goroutines and delivers results on
// C in no particular order. Lines for which decode returns ok=false are skipped and counted.
type Stream[T any] struct {
	C <-chan T

	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	skipped atomic.Int64
}

func NewStream[T any](ctx context.Context, path string, opts StreamOptions, filter func(line []byte) bool, decode func(line []byte) (T, bool)) *Stream[T] {
	ctx, cancel := context.WithCancel(ctx)
	workers := max(opts.Workers, 1)
	lines := make(chan []byte, 4*workers)
	out := make(chan T, 4*workers)
	s := &Stream[T]{C: out, cancel: cancel, done: make(chan struct{})}

	var readErr error
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(lines)
		readErr = ReadLines(ctx, path, opts.ProgressInterval, opts.OnProgress, func(line []byte) (bool, error) {
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
	}()

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for line := range lines {
				v, ok := decode(line)
				if !ok {
					s.skipped.Add(1)
					continue
				}
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
		// Workers can exit early on cancellation while the reader is still unwinding.
		<-readerDone
		s.err = readErr
		cancel()
		close(s.done)
	}()
	return s
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

// Skipped is the number of lines that passed the filter but were rejected by decode.
func (s *Stream[T]) Skipped() int64 { return s.skipped.Load() }
