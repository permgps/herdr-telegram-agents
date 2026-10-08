package logging

import (
	"io"
	"time"
)

// RotateRetry exposes the retry spacing to the external tests.
const RotateRetry = rotateRetry

// SetClockAndErrOut replaces the writer's clock and its sink for rotation
// failures.
func (w *RotatingWriter) SetClockAndErrOut(now func() time.Time, errOut io.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now, w.errOut = now, errOut
}
