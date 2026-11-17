package cli

import (
	"bufio"
	"io"
	"os"
)

// lastLines reads a fixed file-size snapshot and keeps a trailing incomplete
// line for poll to join with the next append.
func lastLines(f *os.File, n int) ([]string, string, int64, error) {
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
	if _, err := f.ReadAt(lastByte[:], end-1); err != nil {
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
