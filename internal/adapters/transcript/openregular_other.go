//go:build !unix

package transcript

// openNonblock is zero off Unix: there is no O_NONBLOCK, and no FIFO in a
// directory to block an open.
const openNonblock = 0
