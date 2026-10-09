package domain

import (
	"context"
	"errors"
)

// MultiReplySource tries each source in order and returns the first reply
// that isn't ErrNoReply. Every source is expected to reject agent kinds it
// doesn't understand with ErrNoReply, so this never needs to dispatch by
// kind itself; order among sources that could both answer does not matter
// in practice because no two sources currently claim the same kind.
// When every source fails, ErrReplyPending from any of them wins over the
// plain ErrNoReply of the others: the reader that owns the agent's kind
// said "not yet", and a later reader's "unsupported agent" must not hide
// it.
type MultiReplySource []ReplySource

// LastReply implements ReplySource.
func (m MultiReplySource) LastReply(ctx context.Context, agent Agent) (Reply, error) {
	lastErr := error(ErrNoReply)
	var pending error
	var lastReal error
	for _, s := range m {
		if err := ctx.Err(); err != nil {
			return Reply{}, err
		}
		r, err := s.LastReply(ctx, agent)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Reply{}, ctxErr
		}
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, ErrNoReply) {
			return Reply{}, err
		}
		if pending == nil && errors.Is(err, ErrReplyPending) {
			pending = err
		}
		// A source that understands the kind but could not read it knows
		// more than the later sources' "not my kind": keep it so the
		// caller can tell a broken read from a kind nobody reads.
		if !errors.Is(err, ErrUnsupportedAgent) {
			lastReal = err
		}
		lastErr = err
	}
	if pending != nil {
		return Reply{}, pending
	}
	if lastReal != nil {
		return Reply{}, lastReal
	}
	return Reply{}, lastErr
}
