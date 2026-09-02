package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Connect builds the daemon's Telegram side from the saved config: bot
// client, token check (dropping pending updates, since the daemon owns
// polling from here on), icon pack, serial queue and gateway. The returned
// run function polls and serves the queue until its context ends. fatal is
// invoked by the poller on 401/409. Extra opts are for tests.
func Connect(ctx context.Context, cfg domain.Config, log *slog.Logger, fatal context.CancelFunc, opts ...bot.Option) (*Gateway, func(context.Context), error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	api, err := NewBot(cfg.BotToken, log, fatal, opts...)
	if err != nil {
		return nil, nil, err
	}
	identity, err := Check(ctx, api, log)
	if err != nil {
		return nil, nil, fmt.Errorf("telegram check: %w", err)
	}
	icons, err := LoadIcons(ctx, api, log)
	if err != nil {
		return nil, nil, fmt.Errorf("telegram icons: %w", err)
	}
	queue := NewQueue(log, DefaultQueueConfig())
	gw := NewGateway(api, Config{ChatID: cfg.ChatID, Operators: cfg.OperatorIDs, Icons: icons, BotID: identity.ID}, queue, log)
	run := func(ctx context.Context) {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			Poll(ctx, api, log)
		}()
		go func() {
			defer wg.Done()
			gw.Run(ctx)
		}()
		wg.Wait()
	}
	log.Info("telegram connected", slog.Int64("bot_id", identity.ID), slog.String("username", identity.Username), slog.Int64("chat_id", cfg.ChatID))
	return gw, run, nil
}
