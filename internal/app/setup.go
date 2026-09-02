package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// setupWait is how long the wizard waits for a group promotion before
	// printing a hint; it keeps waiting afterwards until ctx ends.
	setupWait = 10 * time.Minute
	// setupSiblingWait is the pause after the first candidate to gather
	// others before asking the user to choose.
	setupSiblingWait = 3 * time.Second
	// setupTokenTries bounds the number of rejected tokens.
	setupTokenTries = 3
)

// ErrSetupCancelled means the user declined to save.
var ErrSetupCancelled = errors.New("setup cancelled")

// ProbeFactory builds a SetupProbe for a token. The probe may implement
// io.Closer; Setup closes it when done.
type ProbeFactory func(token string) (domain.SetupProbe, error)

// Setup is the interactive wizard: validate a bot token, wait until the bot
// is promoted in a forum group, record group and operator, save config.
type Setup struct {
	store domain.ConfigStore
	probe ProbeFactory
	ui    domain.SetupUI
	clock domain.Clock
	log   *slog.Logger

	Wait        time.Duration
	SiblingWait time.Duration
}

// NewSetup wires the wizard.
func NewSetup(store domain.ConfigStore, probe ProbeFactory, ui domain.SetupUI, clock domain.Clock, log *slog.Logger) *Setup {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Setup{store: store, probe: probe, ui: ui, clock: clock, log: log, Wait: setupWait, SiblingWait: setupSiblingWait}
}

// Run drives the wizard. It returns the effective config and whether a new
// one was saved (false when the user kept the existing configuration).
func (s *Setup) Run(ctx context.Context) (domain.Config, bool, error) {
	s.log.Info("setup started")
	existing, err := s.store.Load(ctx)
	switch {
	case err == nil:
		s.ui.Print(fmt.Sprintf("Currently configured: group %q (chat %d), operator ids %v.", existing.ChatTitle, existing.ChatID, existing.OperatorIDs))
		reconfigure, err := s.ui.Confirm("Reconfigure with a new token and group? [y/N]")
		if err != nil {
			return domain.Config{}, false, err
		}
		if !reconfigure {
			s.ui.Print("Keeping the current configuration.")
			s.log.Info("setup kept existing config", slog.Int64("chat_id", existing.ChatID))
			return existing, false, nil
		}
	case errors.Is(err, domain.ErrNotConfigured):
		s.log.Debug("setup: no usable config yet", slog.String("reason", err.Error()))
	default:
		return domain.Config{}, false, fmt.Errorf("load config: %w", err)
	}

	token, identity, probe, err := s.askToken(ctx)
	if err != nil {
		return domain.Config{}, false, err
	}
	if c, ok := probe.(interface{ Close() error }); ok {
		defer c.Close()
	}
	s.ui.Print(fmt.Sprintf("Token accepted: @%s.", identity.Username))
	s.ui.Print("Now add the bot to your Telegram forum group (a supergroup with Topics enabled)")
	s.ui.Print("as an administrator with the \"Manage topics\" right. Waiting for that to happen...")

	candidate, err := s.pickCandidate(ctx, probe)
	if err != nil {
		return domain.Config{}, false, err
	}
	cfg := domain.Config{
		Version:      domain.ConfigVersion,
		BotToken:     token,
		BotUsername:  identity.Username,
		ChatID:       candidate.ChatID,
		ChatTitle:    candidate.Title,
		OperatorIDs:  []int64{candidate.FromID},
		LogLevel:     existing.LogLevel,
		ConfiguredAt: s.clock.Now(),
	}
	if err := s.store.Save(ctx, cfg); err != nil {
		return domain.Config{}, false, fmt.Errorf("save config: %w", err)
	}
	s.ui.Print(fmt.Sprintf("Saved: group %q (chat %d), operator %s.", cfg.ChatTitle, cfg.ChatID, describeUser(candidate)))
	s.log.Info("setup saved", slog.Int64("chat_id", cfg.ChatID), slog.Int64("operator_id", candidate.FromID))
	return cfg, true, nil
}

// askToken prompts for a token until Telegram accepts it (at most
// setupTokenTries attempts) and returns the live probe for it.
func (s *Setup) askToken(ctx context.Context) (string, domain.BotIdentity, domain.SetupProbe, error) {
	for try := 1; try <= setupTokenTries; try++ {
		token, err := s.ui.AskSecret("Bot token from @BotFather")
		if err != nil {
			return "", domain.BotIdentity{}, nil, err
		}
		token = strings.TrimSpace(token)
		if token == "" {
			s.ui.Print("The token is empty.")
			continue
		}
		probe, err := s.probe(token)
		if err != nil {
			return "", domain.BotIdentity{}, nil, fmt.Errorf("telegram client: %w", err)
		}
		identity, err := probe.Identity(ctx)
		if err == nil {
			s.log.Info("setup token accepted", slog.Int64("bot_id", identity.ID), slog.String("username", identity.Username))
			return token, identity, probe, nil
		}
		closeProbe(probe)
		if !errors.Is(err, domain.ErrBotUnauthorized) {
			return "", domain.BotIdentity{}, nil, fmt.Errorf("identity: %w", err)
		}
		s.log.Warn("setup token rejected", slog.Int("try", try))
		s.ui.Print("Telegram rejected this token. Check it in @BotFather and try again.")
	}
	return "", domain.BotIdentity{}, nil, fmt.Errorf("%w: too many rejected tokens", ErrSetupCancelled)
}

// pickCandidate waits for promotions, gathers siblings that arrive within
// SiblingWait of the first, and lets the user confirm or choose.
func (s *Setup) pickCandidate(ctx context.Context, probe domain.SetupProbe) (domain.GroupCandidate, error) {
	candidates, err := probe.Candidates(ctx)
	if err != nil {
		return domain.GroupCandidate{}, fmt.Errorf("watch updates: %w", err)
	}
	var found []domain.GroupCandidate
	hint := s.clock.After(s.Wait)
	var siblings <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return domain.GroupCandidate{}, ctx.Err()
		case c, ok := <-candidates:
			if !ok {
				return domain.GroupCandidate{}, fmt.Errorf("telegram updates stopped: %w", ErrSetupCancelled)
			}
			s.log.Debug("setup candidate", slog.Int64("chat_id", c.ChatID), slog.Int64("from_id", c.FromID))
			s.ui.Print(fmt.Sprintf("Seen: %q (chat %d), promoted by %s.", c.Title, c.ChatID, describeUser(c)))
			found = append(found, c)
			if siblings == nil {
				siblings = s.clock.After(s.SiblingWait)
			}
		case <-hint:
			hint = nil
			s.ui.Print("Nothing seen yet. If the bot was added before you ran setup, remove its admin rights and grant them again.")
		case <-siblings:
			return s.choose(found)
		}
	}
}

func (s *Setup) choose(found []domain.GroupCandidate) (domain.GroupCandidate, error) {
	if len(found) == 1 {
		c := found[0]
		ok, err := s.ui.Confirm(fmt.Sprintf("Group %q, operator %s. Save? [y/N]", c.Title, describeUser(c)))
		if err != nil {
			return domain.GroupCandidate{}, err
		}
		if !ok {
			return domain.GroupCandidate{}, ErrSetupCancelled
		}
		return c, nil
	}
	options := make([]string, len(found))
	for i, c := range found {
		options[i] = fmt.Sprintf("%q (chat %d), operator %s", c.Title, c.ChatID, describeUser(c))
	}
	idx, err := s.ui.Choose("Several groups promoted the bot. Which one should be mirrored?", options)
	if err != nil {
		return domain.GroupCandidate{}, err
	}
	if idx < 0 || idx >= len(found) {
		return domain.GroupCandidate{}, ErrSetupCancelled
	}
	return found[idx], nil
}

func describeUser(c domain.GroupCandidate) string {
	if c.FromUsername != "" {
		return fmt.Sprintf("@%s (id %d)", c.FromUsername, c.FromID)
	}
	return fmt.Sprintf("id %d", c.FromID)
}

func closeProbe(p domain.SetupProbe) {
	if c, ok := p.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}
