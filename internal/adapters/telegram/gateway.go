package telegram

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// forceReplyPlaceholder is the input hint shown with a force_reply.
	forceReplyPlaceholder = "text for the agent"
	// downloadTimeout bounds one file download (getFile plus the GET).
	downloadTimeout = 60 * time.Second
	// topicNameMax is Telegram's limit for a forum topic name.
	topicNameMax = 128
	// eventBuffer bounds inbound events waiting for the application.
	eventBuffer = 64
	// NoticeDelay is how long a topic edit notice made by the bot stays
	// before it is deleted until the options apply (SetNoticeDelay).
	// Telegram clients learn a topic's new icon or name from that service
	// message; deleting it at once left phones showing the old icon, and
	// the earlier 10 s left Telegram Desktop on the old icon on
	// 2026-09-06, so the notice now stays 20 s by default.
	NoticeDelay = 20 * time.Second
	// noticeKeep is the noticeDelay value meaning "never delete".
	noticeKeep = -1
	// strangerEvery bounds how often one unknown sender is reported to
	// the application; every one of their updates is still dropped.
	strangerEvery = 10 * time.Minute
)

// Config selects the forum group and who may talk to the bot in it.
type Config struct {
	RejectBefore time.Time
	ChatID       int64
	Operators    []int64
	// Observers may read the group and use /status and /help in General;
	// everything else from them is dropped.
	Observers []int64
	Icons     IconSet
	// BotID is the bot's own user id, needed for the rights check.
	BotID int64
	// NoticeDelay defers the deletion of the bot's own topic edit notices;
	// zero deletes them as soon as they arrive. Production uses the
	// NoticeDelay constant until the options apply through SetNoticeDelay.
	NoticeDelay time.Duration
}

// access is the pair of allow-lists in force, swapped as one value by
// SetAccess so a handler never sees half an update.
type access struct {
	operators map[int64]bool
	observers map[int64]bool
}

// newAccess builds the lists from the two id slices.
func newAccess(operators, observers []int64) *access {
	a := &access{operators: make(map[int64]bool, len(operators)), observers: make(map[int64]bool, len(observers))}
	for _, id := range operators {
		a.operators[id] = true
	}
	for _, id := range observers {
		a.observers[id] = true
	}
	return a
}

// role classifies a sender; an id on both lists is an operator.
func (a *access) role(id int64) domain.Role {
	switch {
	case a.operators[id]:
		return domain.RoleOperator
	case a.observers[id]:
		return domain.RoleObserver
	}
	return domain.RoleStranger
}

// Gateway implements domain.TelegramGateway on top of one bot client, a
// serial call queue and the update handlers registered at construction.
type Gateway struct {
	privateNotices chan int64
	private        privateIngress
	cleanupMu      sync.Mutex
	cleanupPending map[domain.MessageAddress]bool
	rejectBefore   time.Time
	api            *bot.Bot
	chatID         int64
	botID          int64
	// access holds the operator and observer lists in force (SetAccess).
	access atomic.Pointer[access]
	icons  IconSet
	// statusIcons is the emoji per status in force (SetStatusIcons);
	// iconWarned lists emoji already reported as missing from the pack.
	iconMu        sync.RWMutex
	statusIcons   domain.StatusIcons
	iconWarned    map[string]bool
	queue         *Queue
	privateQueues []*Queue
	events        chan domain.Event
	stopped       chan struct{}
	log           *slog.Logger
	// http fetches file bytes from the file endpoint; downloads bypass
	// the message queue because they are not Bot API method calls.
	http *http.Client
	// noticeDelay is the delay in nanoseconds before the bot's own topic
	// notices are deleted, or noticeKeep to leave them in place; set from
	// Config.NoticeDelay and replaced by SetNoticeDelay.
	noticeDelay atomic.Int64
	noticeSleep func(context.Context, time.Duration) error
	// deleteWarned is set after the first failed service-message deletion
	// so a missing right is reported once, not per edit.
	deleteWarned atomic.Bool
	// lastStranger remembers when each unknown sender was last reported
	// (StrangerSeen), so a chatty stranger yields one event per
	// strangerEvery.
	strangerMu   sync.Mutex
	lastStranger map[int64]time.Time
}

var _ domain.TelegramGateway = (*Gateway)(nil)

// Identity probes current bot capabilities through the running queue. A
// failed lookup returns no usable capability rather than a stale allowance.
func (g *Gateway) Identity(ctx context.Context) (domain.BotIdentity, error) {
	var identity domain.BotIdentity
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		me, err := g.api.GetMe(ctx)
		if err != nil {
			return translate(err)
		}
		identity = domain.BotIdentity{ID: me.ID, Username: me.Username, HasTopicsEnabled: me.HasTopicsEnabled}
		return nil
	})
	if err != nil {
		g.log.Warn("private topics capability probe failed")
		return domain.BotIdentity{}, err
	}
	g.log.Debug("private topics capability probed", slog.Int64("bot_id", identity.ID), slog.Bool("enabled", identity.HasTopicsEnabled))
	return identity, nil
}

// NewGateway wires the gateway and registers its update handlers on api, so
// polling may start right after; call Run to serve outbound calls.
func NewGateway(api *bot.Bot, cfg Config, queue *Queue, log *slog.Logger) *Gateway {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Gateway{
		privateNotices: make(chan int64, 32),
		api:            api,
		rejectBefore:   cfg.RejectBefore,
		private:        privateIngress{started: queue.cfg.Now(), seen: map[int64]bool{}},
		chatID:         cfg.ChatID,
		botID:          cfg.BotID,
		icons:          cfg.Icons,
		statusIcons:    domain.DefaultStatusIcons(),
		iconWarned:     map[string]bool{},
		queue:          queue,
		events:         make(chan domain.Event, eventBuffer),
		stopped:        make(chan struct{}),
		log:            log,
		http:           &http.Client{Timeout: downloadTimeout},
		lastStranger:   map[int64]time.Time{},
	}
	g.access.Store(newAccess(cfg.Operators, cfg.Observers))
	for range 4 {
		cfg := queue.cfg
		cfg.MaxTries = 1
		cfg.CallTimeout = 2 * time.Second
		g.privateQueues = append(g.privateQueues, NewQueue(log, cfg))
	}
	g.noticeSleep = sleep
	g.noticeDelay.Store(int64(cfg.NoticeDelay))
	g.registerHandlers()
	return g
}

// SetNoticeDelay replaces the delay applied to the bot's own topic notices
// that arrive from now on; del false keeps them in place.
func (g *Gateway) SetNoticeDelay(delay time.Duration, del bool) {
	if !del {
		g.noticeDelay.Store(noticeKeep)
		g.log.Info("telegram notice delay set", slog.Bool("keep", true))
		return
	}
	g.noticeDelay.Store(int64(delay))
	g.log.Info("telegram notice delay set", slog.Int64("delay_ms", delay.Milliseconds()))
}

// SetAccess replaces the operator and observer lists for updates that
// arrive from now on.
func (g *Gateway) SetAccess(operators, observers []int64) {
	g.access.Store(newAccess(operators, observers))
	g.log.Info("telegram access set", slog.Int("operators", len(operators)), slog.Int("observers", len(observers)))
}

// Run serves the outbound call queue until ctx is done. Inbound handlers
// stop emitting once it returns; the events channel is never closed because
// the poller may still be delivering updates.
func (g *Gateway) Run(ctx context.Context) {
	a := g.access.Load()
	g.log.Info("telegram gateway started", slog.Int64("chat_id", g.chatID), slog.Int("operators", len(a.operators)), slog.Int("observers", len(a.observers)))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); g.runPrivateNotices(ctx) }()
	for _, q := range g.privateQueues {
		wg.Add(1)
		go func() { defer wg.Done(); q.Run(ctx) }()
	}
	g.queue.Run(ctx)
	wg.Wait()
	close(g.stopped)
	g.log.Info("telegram gateway stopped")
}

// Events returns inbound topic and membership events.
func (g *Gateway) Events() <-chan domain.Event { return g.events }

// SetStatusIcons replaces the emoji used for topic icons from now on and
// logs which of them the pack resolves; unresolved ones leave an edited
// topic's icon unchanged and give a created topic only its colour.
func (g *Gateway) SetStatusIcons(icons domain.StatusIcons) {
	g.iconMu.Lock()
	g.statusIcons = icons
	g.iconWarned = map[string]bool{}
	g.iconMu.Unlock()
	var unresolved []string
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if _, ok := g.icons.ID(icons.For(st)); !ok {
			unresolved = append(unresolved, string(st))
		}
	}
	g.log.Info("status icons set",
		slog.String("working", icons.Working), slog.String("idle", icons.Idle),
		slog.String("blocked", icons.Blocked), slog.String("done", icons.Done),
		slog.String("unknown", icons.Unknown), slog.String("exited", icons.Exited),
		slog.Any("unresolved", unresolved))
}

// IconPack lists the free topic-icon pack in Telegram's order.
func (g *Gateway) IconPack() []string { return g.icons.Emojis() }

// iconFor resolves the configured emoji of a status to an Icon and warns
// once per emoji when the pack lacks it.
func (g *Gateway) iconFor(status domain.Status) Icon {
	g.iconMu.RLock()
	emoji := g.statusIcons.For(status)
	g.iconMu.RUnlock()
	icon := g.icons.ForEmoji(emoji, status)
	if icon.EmojiID == "" && emoji != "" {
		g.iconMu.Lock()
		warned := g.iconWarned[emoji]
		g.iconWarned[emoji] = true
		g.iconMu.Unlock()
		if !warned {
			g.log.Warn("icon not in pack, topic icon unchanged", slog.String("emoji", emoji), slog.String("status", string(status)))
		}
	}
	return icon
}
