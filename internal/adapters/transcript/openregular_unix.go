//go:build unix

package transcript

import "syscall"

// openNonblock makes openRegular's open return at once. On a regular file
// the flag changes nothing; on a FIFO swapped in after the Lstat, open(2)
// returns instead of waiting for a writer, and the type check refuses it.
const openNonblock = syscall.O_NONBLOCK
