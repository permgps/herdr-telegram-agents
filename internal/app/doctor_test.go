package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type doctorFixture struct {
	configs *testkit.MemConfigStore
	options *testkit.MemOptionsStore
	store   *testkit.MemMappingStore
	proc    *testkit.FakeProcess
	insp    *testkit.FakeInspector
	herdr   *testkit.FakeHerdr
	clock   *testkit.FakeClock
	broken  []string
	brokenE error
	inspErr error
	doc     *app.Doctor
}

// newDoctor returns a fixture where every check is green: a valid config,
// default options, a running daemon that answers, an empty mapping.
func newDoctor(t *testing.T) *doctorFixture {
	t.Helper()
	f := &doctorFixture{
		configs: testkit.NewMemConfigStore(),
		options: testkit.NewMemOptionsStore(),
		store:   testkit.NewMemMappingStore(),
		insp:    testkit.NewFakeInspector(),
		herdr:   testkit.NewFakeHerdr(nil),
		clock:   testkit.NewFakeClock(t0),
	}
	f.proc = testkit.NewFakeProcess(f.clock.Now)
	f.configs.Set(domain.Config{Version: 1, BotToken: "1:x", BotUsername: "agents_bot", ChatID: -1001, ChatTitle: "Agents", OperatorIDs: []int64{1, 2}, LogLevel: "debug"})
	if err := f.proc.Acquire(4242); err != nil {
		t.Fatal(err)
	}
	f.proc.SetAlive(4242, true)
	f.proc.SetStatusLine("version=1.2.3 pid=4242 uptime=1h0m0s agents=2 dropped=0 herdr=ok sync=on cleanup=30d")
	f.clock.Advance(time.Hour)
	f.doc = &app.Doctor{
		Version: "v1.2.3",
		Config:  f.configs,
		Options: f.options,
		Mapping: f.store,
		Broken:  func() ([]string, error) { return f.broken, f.brokenE },
		Pid:     f.proc,
		Alive:   f.proc.Alive,
		ControlStatus: func(ctx context.Context) (string, error) {
			return f.proc.Status(ctx)
		},
		Inspector: func(domain.Config) (domain.TelegramInspector, error) {
			if f.inspErr != nil {
				return nil, f.inspErr
			}
			return f.insp, nil
		},
		Herdr:            f.herdr,
		ExpectedProtocol: 17,
		Timeout:          50 * time.Millisecond,
		Clock:            f.clock,
	}
	return f
}

func levels(checks []domain.Check) string {
	var parts []string
	for _, c := range checks {
		parts = append(parts, c.Mark()+c.Name)
	}
	return strings.Join(parts, " ")
}

func check(t *testing.T, checks []domain.Check, name string) domain.Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing in %v", name, checks)
	return domain.Check{}
}

func TestDoctorAllGreen(t *testing.T) {
	f := newDoctor(t)
	checks := f.doc.Run(context.Background())
	if got := levels(checks); got != "✓config ✓options ✓telegram ✓group ✓herdr ✓daemon ✓mapping" {
		t.Fatalf("levels = %s", got)
	}
	want := map[string]string{
		"config":   `config.json v1: @agents_bot, chat "Agents" (-1001), 2 operators, log level debug`,
		"options":  "defaults",
		"telegram": "@fakebot (id 42)",
		"group":    `"Agents": forum yes, admin yes, manage topics yes, delete messages yes`,
		"herdr":    "version fake, protocol 17",
		"daemon":   "running (pid 4242, up 1h0m0s): version=1.2.3 pid=4242 uptime=1h0m0s agents=2 dropped=0 herdr=ok sync=on cleanup=30d",
		"mapping":  "0 entries (0 live, 0 exited, 0 muted)",
	}
	for name, detail := range want {
		if got := check(t, checks, name).Detail; got != detail {
			t.Errorf("%s detail = %q, want %q", name, got, detail)
		}
	}
	report := app.RenderChecks("v1.2.3", checks)
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if lines[0] != "Telegram Agents doctor v1.2.3" || len(lines) != 9 || lines[8] != "7 ok, 0 warnings, 0 failures" || !strings.HasPrefix(lines[1], "✓ config: ") {
		t.Fatalf("report:\n%s", report)
	}
}

func TestDoctorFailures(t *testing.T) {
	t.Run("no config skips telegram", func(t *testing.T) {
		f := newDoctor(t)
		f.configs = testkit.NewMemConfigStore()
		f.doc.Config = f.configs
		checks := f.doc.Run(context.Background())
		if got := levels(checks); got != "✗config ✓options ✗telegram ✗group ✓herdr ✓daemon ✓mapping" {
			t.Fatalf("levels = %s", got)
		}
		if c := check(t, checks, "config"); !strings.Contains(c.Detail, "run the setup action") {
			t.Errorf("config detail = %q", c.Detail)
		}
		if len(f.insp.Calls()) != 0 {
			t.Errorf("inspector called without config: %v", f.insp.Calls())
		}
	})
	t.Run("token rejected", func(t *testing.T) {
		f := newDoctor(t)
		f.insp.SetIdentity(domain.BotIdentity{}, domain.ErrBotUnauthorized)
		c := check(t, f.doc.Run(context.Background()), "telegram")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "token rejected") {
			t.Errorf("telegram = %+v", c)
		}
	})
	t.Run("admin without delete warns", func(t *testing.T) {
		f := newDoctor(t)
		f.insp.SetGroup(domain.GroupInfo{Title: "Agents", Rights: domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true}}, nil)
		c := check(t, f.doc.Run(context.Background()), "group")
		if c.Level != domain.CheckWarn || !strings.Contains(c.Detail, "delete messages no") || !strings.Contains(c.Detail, "topic cleanup") {
			t.Errorf("group = %+v", c)
		}
	})
	t.Run("not a forum fails", func(t *testing.T) {
		f := newDoctor(t)
		f.insp.SetGroup(domain.GroupInfo{Title: "Chat", Rights: domain.Rights{IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true}}, nil)
		c := check(t, f.doc.Run(context.Background()), "group")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "enable Topics") {
			t.Errorf("group = %+v", c)
		}
	})
	t.Run("kicked", func(t *testing.T) {
		f := newDoctor(t)
		f.insp.SetGroup(domain.GroupInfo{}, domain.ErrForbidden)
		c := check(t, f.doc.Run(context.Background()), "group")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "not in the group") {
			t.Errorf("group = %+v", c)
		}
	})
	t.Run("protocol mismatch warns", func(t *testing.T) {
		f := newDoctor(t)
		f.herdr.SetPing(domain.HerdrInfo{Version: "0.9.0", Protocol: 16})
		c := check(t, f.doc.Run(context.Background()), "herdr")
		if c.Level != domain.CheckWarn || c.Detail != "version 0.9.0, protocol 16 (plugin built for 17)" {
			t.Errorf("herdr = %+v", c)
		}
	})
	t.Run("socket dead", func(t *testing.T) {
		f := newDoctor(t)
		f.herdr.FailNext("ping", errors.New("dial unix: no such file"))
		c := check(t, f.doc.Run(context.Background()), "herdr")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "no such file") {
			t.Errorf("herdr = %+v", c)
		}
	})
	t.Run("daemon not running", func(t *testing.T) {
		f := newDoctor(t)
		_ = f.proc.Release()
		c := check(t, f.doc.Run(context.Background()), "daemon")
		if c.Level != domain.CheckWarn || c.Detail != "not running (start action)" {
			t.Errorf("daemon = %+v", c)
		}
	})
	t.Run("stale pid", func(t *testing.T) {
		f := newDoctor(t)
		f.proc.SetAlive(4242, false)
		c := check(t, f.doc.Run(context.Background()), "daemon")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "stale pid file for 4242") {
			t.Errorf("daemon = %+v", c)
		}
	})
	t.Run("control unavailable", func(t *testing.T) {
		f := newDoctor(t)
		f.proc.SetControlUnavailable(true)
		c := check(t, f.doc.Run(context.Background()), "daemon")
		if c.Level != domain.CheckFail || !strings.Contains(c.Detail, "not answering on the control channel") {
			t.Errorf("daemon = %+v", c)
		}
	})
	t.Run("broken mapping backups", func(t *testing.T) {
		f := newDoctor(t)
		f.broken = []string{"mapping.json.broken-20260903T120000"}
		m := domain.NewMapping(-1001)
		a := agent("p1", "t1", "x", domain.StatusWorking)
		m.Link(a.Key, domain.Topic{ThreadID: 5}, a, t0)
		if err := f.store.Save(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		c := check(t, f.doc.Run(context.Background()), "mapping")
		if c.Level != domain.CheckWarn || !strings.HasPrefix(c.Detail, "1 entry (1 live, 0 exited, 0 muted); corrupt copies moved aside: mapping.json.broken-") {
			t.Errorf("mapping = %+v", c)
		}
	})
	t.Run("options dropped", func(t *testing.T) {
		f := newDoctor(t)
		bad, _ := domain.DefaultOptions().With(domain.OptionDeleteAfterDays, "lots")
		good, _ := bad.With(domain.OptionRedact, "false")
		f.options.Set(good)
		c := check(t, f.doc.Run(context.Background()), "options")
		if c.Level != domain.CheckWarn || c.Detail != "1 value set; invalid values ignored: topics.delete_after_days" {
			t.Errorf("options = %+v", c)
		}
	})
	t.Run("hanging check is cut by the timeout", func(t *testing.T) {
		f := newDoctor(t)
		f.insp.Block("identity")
		start := time.Now()
		c := check(t, f.doc.Run(context.Background()), "telegram")
		if c.Level != domain.CheckFail || time.Since(start) > 2*time.Second {
			t.Errorf("telegram = %+v after %s", c, time.Since(start))
		}
	})
}

func TestSendTest(t *testing.T) {
	insp := testkit.NewFakeInspector()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	msg, err := app.SendTest(context.Background(), insp, "v1.2.3", now, nil)
	if err != nil || msg != "send-test: delivered to General (message 500)" {
		t.Fatalf("SendTest = %q, %v", msg, err)
	}
	if calls := insp.Calls(); len(calls) != 1 || calls[0] != "send-test:🔔 Telegram Agents v1.2.3: test message from the send-test action (2026-09-03 12:00 UTC)" {
		t.Fatalf("calls = %v", calls)
	}
	insp.SetSend(0, domain.ErrBotUnauthorized)
	if _, err := app.SendTest(context.Background(), insp, "", now, nil); err == nil || err.Error() != "send-test failed: token rejected, run the setup action" {
		t.Fatalf("err = %v", err)
	}
	insp.SetSend(0, errors.New("telegram api 400: chat not found"))
	if _, err := app.SendTest(context.Background(), insp, "", now, nil); err == nil || err.Error() != "send-test failed: telegram api 400: chat not found" {
		t.Fatalf("err = %v", err)
	}
}
