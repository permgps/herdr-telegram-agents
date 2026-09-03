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

// doctorCheckTimeout bounds one doctor check so a hanging socket or API
// never blocks the pane.
const doctorCheckTimeout = 5 * time.Second

// Doctor runs the diagnostic checks behind the doctor action: config,
// options, Telegram token, group rights, Herdr socket, daemon and mapping,
// in that order, each bounded by Timeout. Every dependency is a port or a
// small function so the checks run against fakes in tests.
type Doctor struct {
	Version string
	Config  domain.ConfigStore
	Options domain.OptionsStore
	Mapping domain.MappingStore
	// Broken lists the mapping.json.broken-* backups; nil means none.
	Broken func() ([]string, error)
	Pid    domain.PidFile
	Alive  func(pid int) bool
	// ControlStatus asks the running daemon for its status line.
	ControlStatus func(ctx context.Context) (string, error)
	// Inspector builds the light Telegram client from the loaded config.
	Inspector func(cfg domain.Config) (domain.TelegramInspector, error)
	Herdr     domain.HerdrProber
	// ExpectedProtocol is the socket protocol the adapter was written for.
	ExpectedProtocol int
	Choices          domain.ChoiceSource
	Timeout          time.Duration
	Clock            domain.Clock
	Log              *slog.Logger
}

// Run performs every check and returns them in report order.
func (d *Doctor) Run(ctx context.Context) []domain.Check {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Timeout <= 0 {
		d.Timeout = doctorCheckTimeout
	}
	var checks []domain.Check
	add := func(c domain.Check) {
		d.Log.Info("doctor check", slog.String("name", c.Name), slog.String("level", c.Mark()), slog.String("detail", c.Detail))
		checks = append(checks, c)
	}
	cfg, cfgCheck := d.checkConfig(ctx)
	add(cfgCheck)
	add(d.checkOptions(ctx))
	if cfgCheck.Level == domain.CheckFail {
		add(domain.Check{Name: "telegram", Level: domain.CheckFail, Detail: "skipped: no config"})
		add(domain.Check{Name: "group", Level: domain.CheckFail, Detail: "skipped: no config"})
	} else {
		insp, err := d.Inspector(cfg)
		if err != nil {
			add(domain.Check{Name: "telegram", Level: domain.CheckFail, Detail: "client: " + failureReason(err)})
			add(domain.Check{Name: "group", Level: domain.CheckFail, Detail: "skipped: no client"})
		} else {
			add(d.checkTelegram(ctx, insp))
			add(d.checkGroup(ctx, insp))
		}
	}
	add(d.checkHerdr(ctx))
	add(d.checkDaemon(ctx))
	add(d.checkMapping(ctx))
	ok, warn, fail := domain.Summarize(checks)
	d.Log.Info("doctor done", slog.Int("ok", ok), slog.Int("warn", warn), slog.Int("fail", fail))
	return checks
}

func (d *Doctor) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.Timeout)
}

func (d *Doctor) checkConfig(ctx context.Context) (domain.Config, domain.Check) {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	cfg, err := d.Config.Load(cctx)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		if errors.Is(err, domain.ErrNotConfigured) {
			return cfg, domain.Check{Name: "config", Level: domain.CheckFail, Detail: "not configured, run the setup action (" + failureReason(err) + ")"}
		}
		return cfg, domain.Check{Name: "config", Level: domain.CheckFail, Detail: "unreadable: " + failureReason(err)}
	}
	bot := "bot id unknown"
	if cfg.BotUsername != "" {
		bot = "@" + cfg.BotUsername
	}
	level := cfg.LogLevel
	if level == "" {
		level = "info"
	}
	detail := fmt.Sprintf("config.json v%d: %s, chat %q (%d), %s, log level %s",
		cfg.Version, bot, cfg.ChatTitle, cfg.ChatID, plural(len(cfg.OperatorIDs), "operator"), level)
	return cfg, domain.Check{Name: "config", Level: domain.CheckOK, Detail: detail}
}

func (d *Doctor) checkOptions(ctx context.Context) domain.Check {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	opts, err := d.Options.Load(cctx)
	if err != nil {
		return domain.Check{Name: "options", Level: domain.CheckFail, Detail: "options.json unreadable, defaults in force: " + failureReason(err)}
	}
	clean, dropped := domain.SanitizeOptions(opts, d.Choices)
	set := 0
	for _, spec := range domain.OptionSpecs {
		if !clean.IsDefault(spec.Key) {
			set++
		}
	}
	detail := "defaults"
	if set > 0 {
		detail = plural(set, "value") + " set"
	}
	if len(dropped) > 0 {
		return domain.Check{Name: "options", Level: domain.CheckWarn, Detail: detail + "; invalid values ignored: " + strings.Join(dropped, ", ")}
	}
	return domain.Check{Name: "options", Level: domain.CheckOK, Detail: detail}
}

func (d *Doctor) checkTelegram(ctx context.Context, insp domain.TelegramInspector) domain.Check {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	id, err := insp.Identity(cctx)
	switch {
	case errors.Is(err, domain.ErrBotUnauthorized):
		return domain.Check{Name: "telegram", Level: domain.CheckFail, Detail: "token rejected (401), run the setup action"}
	case err != nil:
		return domain.Check{Name: "telegram", Level: domain.CheckFail, Detail: "getMe failed: " + failureReason(err)}
	}
	return domain.Check{Name: "telegram", Level: domain.CheckOK, Detail: fmt.Sprintf("@%s (id %d)", id.Username, id.ID)}
}

func (d *Doctor) checkGroup(ctx context.Context, insp domain.TelegramInspector) domain.Check {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	g, err := insp.Group(cctx)
	switch {
	case errors.Is(err, domain.ErrForbidden):
		return domain.Check{Name: "group", Level: domain.CheckFail, Detail: "bot is not in the group any more, add it again through the setup action"}
	case err != nil:
		return domain.Check{Name: "group", Level: domain.CheckFail, Detail: "lookup failed: " + failureReason(err)}
	}
	r := g.Rights
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	detail := fmt.Sprintf("%q: forum %s, admin %s, manage topics %s, delete messages %s",
		g.Title, yes(r.IsForum), yes(r.IsAdmin), yes(r.CanManageTopics), yes(r.CanDeleteMessages))
	switch {
	case !r.IsForum:
		return domain.Check{Name: "group", Level: domain.CheckFail, Detail: detail + "; enable Topics in the group settings"}
	case !r.IsAdmin || !r.CanManageTopics:
		return domain.Check{Name: "group", Level: domain.CheckFail, Detail: detail + "; grant the bot the Manage topics right"}
	case !r.CanDeleteMessages:
		return domain.Check{Name: "group", Level: domain.CheckWarn, Detail: detail + "; topic cleanup and icon notices need Delete messages"}
	}
	return domain.Check{Name: "group", Level: domain.CheckOK, Detail: detail}
}

func (d *Doctor) checkHerdr(ctx context.Context) domain.Check {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	info, err := d.Herdr.Ping(cctx)
	if err != nil {
		return domain.Check{Name: "herdr", Level: domain.CheckFail, Detail: "socket not answering: " + failureReason(err)}
	}
	detail := fmt.Sprintf("version %s, protocol %d", info.Version, info.Protocol)
	if d.ExpectedProtocol != 0 && info.Protocol != d.ExpectedProtocol {
		return domain.Check{Name: "herdr", Level: domain.CheckWarn, Detail: fmt.Sprintf("%s (plugin built for %d)", detail, d.ExpectedProtocol)}
	}
	return domain.Check{Name: "herdr", Level: domain.CheckOK, Detail: detail}
}

func (d *Doctor) checkDaemon(ctx context.Context) domain.Check {
	info, err := d.Pid.Read()
	switch {
	case errors.Is(err, domain.ErrNotRunning):
		return domain.Check{Name: "daemon", Level: domain.CheckWarn, Detail: "not running (start action)"}
	case err != nil:
		return domain.Check{Name: "daemon", Level: domain.CheckFail, Detail: "pid file unreadable: " + failureReason(err)}
	case !d.Alive(info.PID):
		return domain.Check{Name: "daemon", Level: domain.CheckFail, Detail: fmt.Sprintf("stale pid file for %d, run the start action", info.PID)}
	}
	now := time.Now()
	if d.Clock != nil {
		now = d.Clock.Now()
	}
	line := Summary(DaemonStatus{Running: true, PID: info.PID, Since: info.Since}, now)
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	stats, err := d.ControlStatus(cctx)
	switch {
	case errors.Is(err, domain.ErrControlUnavailable):
		return domain.Check{Name: "daemon", Level: domain.CheckFail, Detail: line + ", not answering on the control channel; restart it"}
	case err != nil:
		return domain.Check{Name: "daemon", Level: domain.CheckWarn, Detail: line + ", status failed: " + failureReason(err)}
	case stats == "":
		return domain.Check{Name: "daemon", Level: domain.CheckOK, Detail: line}
	}
	return domain.Check{Name: "daemon", Level: domain.CheckOK, Detail: line + ": " + stats}
}

func (d *Doctor) checkMapping(ctx context.Context) domain.Check {
	cctx, cancel := d.bounded(ctx)
	defer cancel()
	m, err := d.Mapping.Load(cctx)
	if err != nil {
		return domain.Check{Name: "mapping", Level: domain.CheckFail, Detail: "mapping.json unreadable: " + failureReason(err)}
	}
	live, exited, muted := m.Counts()
	entries := fmt.Sprintf("%d entries", len(m.Topics))
	if len(m.Topics) == 1 {
		entries = "1 entry"
	}
	detail := fmt.Sprintf("%s (%d live, %d exited, %d muted)", entries, live, exited, muted)
	if d.Broken == nil {
		return domain.Check{Name: "mapping", Level: domain.CheckOK, Detail: detail}
	}
	broken, err := d.Broken()
	switch {
	case err != nil:
		return domain.Check{Name: "mapping", Level: domain.CheckWarn, Detail: detail + "; backup listing failed: " + failureReason(err)}
	case len(broken) > 0:
		return domain.Check{Name: "mapping", Level: domain.CheckWarn, Detail: detail + "; corrupt copies moved aside: " + strings.Join(broken, ", ")}
	}
	return domain.Check{Name: "mapping", Level: domain.CheckOK, Detail: detail}
}

// RenderChecks renders the doctor report: a header with the version, one
// line per check and a summary line.
func RenderChecks(version string, checks []domain.Check) string {
	var b strings.Builder
	title := notifyTitle + " doctor"
	if version != "" {
		title += " " + version
	}
	b.WriteString(title + "\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "%s %s: %s\n", c.Mark(), c.Name, c.Detail)
	}
	ok, warn, fail := domain.Summarize(checks)
	fmt.Fprintf(&b, "%d ok, %s, %s\n", ok, plural(warn, "warning"), plural(fail, "failure"))
	return b.String()
}

// SendTest posts the test message into General and returns the sentence
// the action reports. The error, when any, already carries the reason.
func SendTest(ctx context.Context, insp domain.TelegramInspector, version string, now time.Time, log *slog.Logger) (string, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	title := notifyTitle
	if version != "" {
		title += " " + version
	}
	text := fmt.Sprintf("🔔 %s: test message from the send-test action (%s)", title, now.UTC().Format("2006-01-02 15:04 UTC"))
	id, err := insp.SendTest(ctx, text)
	if err != nil {
		log.Warn("send-test failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("send-test failed: %s", sendTestReason(err))
	}
	log.Info("send-test", slog.Int("message_id", id))
	return fmt.Sprintf("send-test: delivered to General (message %d)", id), nil
}

func sendTestReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrBotUnauthorized):
		return "token rejected, run the setup action"
	case errors.Is(err, domain.ErrForbidden):
		return "the bot cannot post in the group, add it again through the setup action"
	}
	return failureReason(err)
}
