package transcript

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Reasons openRegular and openRegularSeen fail with; none carries the path.
var (
	errNotRegular  = errors.New("is not a regular file")
	errOpenFailed  = errors.New("could not be opened")
	errUnreadable  = errors.New("could not be read")
	errFileChanged = errors.New("changed while it was opened")
)

// openRegular opens a file below root and checks it is a regular file and
// the same file Lstat saw, so a link planted in its place is refused rather
// than followed. The open does not block: a FIFO swapped in after the Lstat
// opens at once and is refused, instead of waiting for a writer. A missing
// file is fs.ErrNotExist; the returned FileInfo is the open file's own.
func openRegular(root *os.Root, rel string) (*os.File, os.FileInfo, error) {
	seen, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, nil, errUnreadable
	}
	if !seen.Mode().IsRegular() {
		return nil, nil, errNotRegular
	}
	return openRegularSeen(root, rel, seen)
}

// openRegularSeen is openRegular against seen, an Lstat of rel the caller
// took earlier (a search or a directory listing).
func openRegularSeen(root *os.Root, rel string, seen os.FileInfo) (*os.File, os.FileInfo, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, nil, errOpenFailed
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, errUnreadable
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, errNotRegular
	}
	if !os.SameFile(seen, info) {
		f.Close()
		return nil, nil, errFileChanged
	}
	return f, info, nil
}

// openFailure words an openRegular failure for what ("the agy
// transcript"): a missing file stays fs.ErrNotExist so the caller can try
// another, anything else is domain.ErrNoReply with the reason.
func openFailure(what string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fs.ErrNotExist
	}
	return fmt.Errorf("%w: %s %v", domain.ErrNoReply, what, err)
}
