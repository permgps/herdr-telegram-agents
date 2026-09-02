package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Probe implements domain.SetupProbe for the setup wizard: it proves the
// token with getMe and watches my_chat_member updates for a promotion to
// administrator with topic rights in a forum supergroup. Pending updates are
// kept (deleteWebhook without dropping) so a promotion done before setup is
// still seen.
type Probe struct {
	api *bot.Bot
	log *slog.Logger

	mu     sync.Mutex
	seen   map[int64]bool
	out    chan domain.GroupCandidate
	cancel context.CancelFunc // set by Candidates
	done   chan struct{}      // closed when polling ends; nil before Candidates
}

var _ domain.SetupProbe = (*Probe)(nil)

// NewProbe builds the probe for a token. Extra opts are for tests
// (bot.WithServerURL).
func NewProbe(token string, log *slog.Logger, opts ...bot.Option) (*Probe, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p := &Probe{log: log, seen: map[int64]bool{}, out: make(chan domain.GroupCandidate, 16)}
	api, err := NewBot(token, log, p.stop, opts...)
	if err != nil {
		return nil, err
	}
	p.api = api
	api.RegisterHandlerMatchFunc(func(u *models.Update) bool { return u.MyChatMember != nil }, p.onMember)
	return p, nil
}

// Identity validates the token with getMe and clears any webhook while
// keeping pending updates.
//
// deleteWebhook is called with nil params on purpose: with
// DropPendingUpdates false every field is omitted and go-telegram/bot
// v1.25 sends a multipart body that holds nothing but the closing
// boundary, which Telegram answers with an empty body ("unexpected end of
// JSON input", seen 2026-09-02). nil params send no body at all, like getMe.
func (p *Probe) Identity(ctx context.Context) (domain.BotIdentity, error) {
	me, err := p.api.GetMe(ctx)
	if err != nil {
		return domain.BotIdentity{}, fmt.Errorf("getMe: %w", translate(err))
	}
	if _, err := p.api.DeleteWebhook(ctx, nil); err != nil {
		return domain.BotIdentity{}, fmt.Errorf("deleteWebhook: %w", translate(err))
	}
	p.log.Info("setup probe identified bot", slog.Int64("bot_id", me.ID), slog.String("username", me.Username))
	return domain.BotIdentity{ID: me.ID, Username: me.Username}, nil
}

// Candidates starts polling and streams promotions until ctx is done or
// Close is called; the channel is closed afterwards. It may be called once.
func (p *Probe) Candidates(ctx context.Context) (<-chan domain.GroupCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done != nil {
		return nil, errors.New("setup probe already polling")
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel, p.done = cancel, make(chan struct{})
	done := p.done
	go func() {
		defer close(done)
		defer close(p.out)
		defer cancel()
		p.log.Info("setup probe polling for group promotions")
		p.api.Start(runCtx)
		p.log.Debug("setup probe polling stopped")
	}()
	return p.out, nil
}

// Close stops polling and waits for it to end. It is safe before
// Candidates and more than once.
func (p *Probe) Close() error {
	p.stop()
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

func (p *Probe) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Process feeds one update to the probe's handlers, bypassing polling. It
// exists for tests; the daemon never calls it.
func (p *Probe) Process(ctx context.Context, u *models.Update) {
	p.api.ProcessUpdate(ctx, u)
}

// onMember checks one my_chat_member update. The chat's forum flag is
// taken from the update and confirmed with getChat when absent, because
// the flag is optional on the wire.
func (p *Probe) onMember(ctx context.Context, b *bot.Bot, u *models.Update) {
	cm := u.MyChatMember
	if !canManageTopics(cm.NewChatMember) {
		p.log.Debug("setup probe: membership change without topic rights",
			slog.Int64("chat_id", cm.Chat.ID), slog.String("status", string(cm.NewChatMember.Type)))
		return
	}
	title, isForum := cm.Chat.Title, cm.Chat.IsForum
	if !isForum {
		full, err := b.GetChat(ctx, &bot.GetChatParams{ChatID: cm.Chat.ID})
		if err != nil {
			p.log.Warn("setup probe: getChat failed", slog.Int64("chat_id", cm.Chat.ID), slog.String("err", translate(err).Error()))
			return
		}
		isForum = full.IsForum
		if full.Title != "" {
			title = full.Title
		}
	}
	if !isForum {
		p.log.Debug("setup probe: promoted in a chat that is not a forum", slog.Int64("chat_id", cm.Chat.ID))
		return
	}
	p.mu.Lock()
	dup := p.seen[cm.Chat.ID]
	p.seen[cm.Chat.ID] = true
	p.mu.Unlock()
	if dup {
		p.log.Debug("setup probe: candidate already reported", slog.Int64("chat_id", cm.Chat.ID))
		return
	}
	c := domain.GroupCandidate{ChatID: cm.Chat.ID, Title: title, FromID: cm.From.ID, FromUsername: cm.From.Username}
	p.log.Info("setup probe: forum group candidate", slog.Int64("chat_id", c.ChatID), slog.Int64("from_id", c.FromID))
	select {
	case p.out <- c:
	case <-ctx.Done():
		p.log.Warn("setup probe: candidate dropped, context done", slog.Int64("chat_id", c.ChatID))
	}
}
