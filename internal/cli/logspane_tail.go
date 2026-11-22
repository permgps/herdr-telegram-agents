package cli

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
)

// lastLines reads a fixed file-size snapshot and keeps a trailing incomplete
// line for poll to join with the next append.
func lastLines(f *os.File, n int) ([]string, string, int64, error) {
	return lastLinesWithReadAt(f, n, func(b []byte, off int64) (int, error) { return f.ReadAt(b, off) })
}

func lastLinesWithReadAt(f *os.File, n int, readAt func([]byte, int64) (int, error)) ([]string, string, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, "", 0, err
	}
	end := info.Size()
	if n <= 0 || end == 0 {
		return nil, "", end, nil
	}
	ring := make([]string, 0, n+1)
	sc := bufio.NewScanner(io.NewSectionReader(f, 0, end))
	sc.Split(splitLogLines)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(ring) == n+1 {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, "", 0, err
	}
	var lastByte [1]byte
	if _, err := readAt(lastByte[:], end-1); err != nil {
		if errors.Is(err, io.EOF) {
			// The file shrank after the snapshot size was recorded. Drop the
			// partial snapshot and let the tailer poll the current file at 0.
			return nil, "", 0, nil
		}
		return nil, "", 0, err
	}
	var partial string
	if lastByte[0] != '\n' && len(ring) > 0 {
		partial = ring[len(ring)-1]
		ring = ring[:len(ring)-1]
	}
	if len(ring) > n {
		ring = ring[len(ring)-n:]
	}
	return ring, partial, end, nil
}

// splitLogLines preserves a carriage return on an unterminated final token.
// ScanLines strips it even when the snapshot ends before the newline arrives.
func splitLogLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, bytes.TrimSuffix(data[:i], []byte{'\r'}), nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
