package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// testPack is 112 entries like the live pack: the six defaults first, then
// 🔥 and filler, so two grid pages are needed.
func testPack() []string {
	pack := []string{"⚡️", "✅", "❓", "🏆", "👀", "🏁", "🔥", "🤖", "🧠"}
	for len(pack) < 112 {
		pack = append(pack, fmt.Sprintf("x%d", len(pack)))
	}
	return pack
}

func pressPanel(f *bridgeFixture, t *testing.T, id int, data string) {
	t.Helper()
	if err := f.in.PressPanel(f.ctx, domain.ButtonPressed{CallbackID: "cb", ThreadID: 0, MessageID: id, FromID: 1, Data: data}); err != nil {
		t.Fatalf("PressPanel(%s) = %v", data, err)
	}
}

func lastCall(f *bridgeFixture) string {
	calls := f.tg.Calls()
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1]
}

func texts(buttons []domain.Button) []string {
	out := make([]string, 0, len(buttons))
	for _, b := range buttons {
		out = append(out, b.Text)
	}
	return out
}

func TestPanelOpenShowsGroupsAndRetiresPrevious(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.in.HandleGeneral(f.ctx, domain.GeneralCommand{MessageID: 50, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].ThreadID != 0 || !sent[0].HTML || sent[0].ReplyTo != 50 {
		t.Fatalf("Sent = %+v", sent)
	}
	if got := texts(sent[0].Buttons); strings.Join(got, "|") != "Sync|Quiet|Posts|Appearance|Privacy|Topics|✖ Close" {
		t.Errorf("home buttons = %v", got)
	}
	if !strings.Contains(sent[0].Text, "<b>Sync</b>: What the mirror writes to Telegram.") {
		t.Errorf("home text = %s", sent[0].Text)
	}
	f.tg.Reset()
	if err := f.in.HandleGeneral(f.ctx, domain.GeneralCommand{MessageID: 51, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	calls := f.tg.Calls()
	if len(calls) != 2 || calls[0] != "buttons:1000:" || !strings.HasPrefix(calls[1], "send:0:") {
		t.Fatalf("second /options calls = %q", calls)
	}
}

func TestPanelToggleSync(t *testing.T) {
	f := newBridgeFixture(t)
	pressPanel(f, t, 900, dataGroup(0))
	if got := texts(f.tg.Buttons(900)); strings.Join(got, "|") != "☑ Herdr → Telegram sync|↺ Reset to defaults|‹ Back|✖ Close" {
		t.Fatalf("sync group buttons = %v", got)
	}
	if text := f.tg.Text(900); !strings.Contains(text, "<b>Herdr → Telegram sync</b>: Mirror Herdr agents") || !strings.Contains(text, "Current: on") {
		t.Errorf("group text = %s", text)
	}
	f.tg.Reset()
	pressPanel(f, t, 900, dataToggle(domain.OptionSyncEnabled))
	if f.opts.SyncEnabled() || f.options.Saved() != 1 {
		t.Fatalf("toggle did not save: sync=%v saves=%d", f.opts.SyncEnabled(), f.options.Saved())
	}
	calls := f.tg.Calls()
	if len(calls) != 2 || calls[0] != "answer:cb:saved" || !strings.HasPrefix(calls[1], "edittext:900:") {
		t.Fatalf("calls = %q", calls)
	}
	if got := texts(f.tg.Buttons(900))[0]; got != "☐ Herdr → Telegram sync" {
		t.Errorf("button after toggle = %q", got)
	}
	if !strings.Contains(f.tg.Text(900), "Current: off") {
		t.Errorf("text after toggle = %s", f.tg.Text(900))
	}
}

func TestPanelIconGridPickAndDuplicate(t *testing.T) {
	f := newBridgeFixture(t)
	f.tg.SetIconPack(testPack())
	pressPanel(f, t, 900, dataGroup(groupIndex(domain.GroupAppearance)))
	if got := texts(f.tg.Buttons(900)); got[0] != "⚡ working" || got[5] != "🏁 exited" || len(got) != 9 {
		t.Fatalf("appearance buttons = %v", got)
	}

	pressPanel(f, t, 900, dataGrid("icons.working", 0))
	buttons := f.tg.Buttons(900)
	// 56 emoji + nav row (3) + back.
	if len(buttons) != 60 {
		t.Fatalf("grid buttons = %d, want 60", len(buttons))
	}
	if buttons[0].Text != "[⚡️]" || buttons[0].Row != 1 || buttons[7].Row != 1 || buttons[8].Row != 2 || buttons[55].Row != 7 {
		t.Errorf("grid rows wrong: %+v %+v %+v", buttons[0], buttons[8], buttons[55])
	}
	if nav := texts(buttons[56:60]); strings.Join(nav, "|") != "·|1/2|›|‹ Back" || buttons[58].Data != dataGrid("icons.working", 1) {
		t.Errorf("nav = %v (%s)", nav, buttons[58].Data)
	}
	if text := f.tg.Text(900); !strings.Contains(text, "<b>Icon for working</b>") || !strings.Contains(text, panelOnlyPackIcons) || !strings.Contains(text, "Current: ⚡") {
		t.Errorf("grid text = %s", text)
	}

	pressPanel(f, t, 900, dataGrid("icons.working", 1))
	buttons = f.tg.Buttons(900)
	if len(buttons) != 112-56+4 || texts(buttons[len(buttons)-4:])[0] != "‹" {
		t.Errorf("page 2 buttons = %d, %v", len(buttons), texts(buttons[len(buttons)-4:]))
	}

	// ✅ is idle's icon: refused with a toast, nothing saved, grid stays.
	f.tg.Reset()
	pressPanel(f, t, 900, dataPick("icons.working", 1))
	if calls := f.tg.Calls(); calls[0] != "answer:cb:used by idle" || f.options.Saved() != 0 {
		t.Fatalf("duplicate pick: calls=%q saves=%d", calls, f.options.Saved())
	}
	if !strings.Contains(f.tg.Text(900), "<b>Icon for working</b>") {
		t.Error("grid not re-rendered after duplicate")
	}

	// 🔥 is free: saved, gateway told, group view shows it.
	f.tg.Reset()
	pressPanel(f, t, 900, dataPick("icons.working", 6))
	// The gateway is told by the daemon's hook, not by the panel, so the
	// fake's icon table is not checked here (see the daemon tests).
	if calls := f.tg.Calls(); calls[0] != "answer:cb:saved" || f.options.Saved() != 1 {
		t.Fatalf("pick: calls=%q saves=%d", calls, f.options.Saved())
	}
	if got := texts(f.tg.Buttons(900))[0]; got != "🔥 working" {
		t.Errorf("group after pick = %q", got)
	}
	if f.opts.StatusIcons().Working != "🔥" {
		t.Errorf("icons = %+v", f.opts.StatusIcons())
	}

	// Reset restores ⚡.
	f.tg.Reset()
	pressPanel(f, t, 900, dataReset(groupIndex(domain.GroupAppearance)))
	if calls := f.tg.Calls(); calls[0] != "answer:cb:saved" || f.opts.StatusIcons().Working != "⚡" {
		t.Fatalf("reset: calls=%q icons=%+v", calls, f.opts.StatusIcons())
	}
	f.tg.Reset()
	pressPanel(f, t, 900, dataReset(groupIndex(domain.GroupAppearance)))
	if got := lastCall(f); f.tg.Calls()[0] != "answer:cb:already at defaults" {
		t.Errorf("second reset calls = %q (%s)", f.tg.Calls(), got)
	}
}

func TestPanelGridWithoutPack(t *testing.T) {
	f := newBridgeFixture(t)
	f.tg.SetIconPack(nil)
	pressPanel(f, t, 900, dataGrid("icons.idle", 0))
	if got := texts(f.tg.Buttons(900)); strings.Join(got, "|") != "‹ Back" || !strings.Contains(f.tg.Text(900), panelPackUnavailable) {
		t.Errorf("grid without pack: buttons=%v text=%s", got, f.tg.Text(900))
	}
}

func TestPanelCloseAndUnknownData(t *testing.T) {
	f := newBridgeFixture(t)
	_ = f.opts.Set(f.ctx, domain.OptionSyncEnabled, "false", 1)
	pressPanel(f, t, 900, dataClose())
	if calls := f.tg.Calls(); len(calls) != 2 || calls[0] != "answer:cb:" || !strings.HasSuffix(calls[1], ":buttons=0") {
		t.Fatalf("close calls = %q", calls)
	}
	if text := f.tg.Text(900); !strings.Contains(text, "<b>Options</b>") || !strings.Contains(text, "Herdr → Telegram sync: off") || !strings.Contains(text, "working: ⚡") {
		t.Errorf("summary = %s", text)
	}
	for _, data := range []string{"o:", "o:zz", "o:g:x", "o:g:9", "o:t:nope", "o:t:icons.idle", "o:v:icons.idle:999", "o:c:sync.enabled:0", "1", ""} {
		f.tg.Reset()
		pressPanel(f, t, 900, data)
		if calls := f.tg.Calls(); len(calls) != 1 || calls[0] != "answer:cb:unknown button" {
			t.Errorf("data %q: calls = %q", data, calls)
		}
	}
	f.tg.Reset()
	pressPanel(f, t, 900, dataNoop())
	if calls := f.tg.Calls(); len(calls) != 1 || calls[0] != "answer:cb:" {
		t.Errorf("noop calls = %q", calls)
	}
}

func TestPanelEditFailureSendsFreshPanel(t *testing.T) {
	f := newBridgeFixture(t)
	f.tg.FailNext("edittext", errors.New("message to edit not found"))
	pressPanel(f, t, 900, dataHome())
	calls := f.tg.Calls()
	if len(calls) != 3 || !strings.HasPrefix(calls[1], "edittext:900:") || !strings.HasPrefix(calls[2], "send:0:") {
		t.Fatalf("calls = %q", calls)
	}
	if f.in.panel.lastID != 1000 {
		t.Errorf("lastID = %d, want the new message 1000", f.in.panel.lastID)
	}
}

func TestPanelStringsAreEnglish(t *testing.T) {
	for _, s := range []string{panelTitle, panelPickGroup, panelOnlyPackIcons, panelPackUnavailable, panelToastSaved,
		panelToastUsedBy, panelToastNotEdit, panelToastUnknown, panelToastDefaults, panelInGeneral, panelBack, panelClose, panelReset} {
		for _, r := range s {
			if r >= 0x0400 && r <= 0x04FF {
				t.Errorf("panel string %q contains Cyrillic", s)
			}
		}
	}
}

func TestPanelPrivacyAndTopicsGroups(t *testing.T) {
	f := newBridgeFixture(t)
	pressPanel(f, t, 900, dataGroup(groupIndex(domain.GroupPrivacy)))
	if got := texts(f.tg.Buttons(900)); got[0] != "☑ Redact secrets" || len(got) != 4 {
		t.Fatalf("privacy buttons = %v", got)
	}
	pressPanel(f, t, 900, dataToggle(domain.OptionRedact))
	if got := texts(f.tg.Buttons(900)); got[0] != "☐ Redact secrets" || f.options.Saved() != 1 || f.opts.RedactEnabled() {
		t.Fatalf("after toggle: buttons=%v saves=%d", got, f.options.Saved())
	}

	pressPanel(f, t, 900, dataGroup(groupIndex(domain.GroupTopics)))
	if got := texts(f.tg.Buttons(900)); got[0] != "30 days Delete closed topics after" || len(got) != 4 {
		t.Fatalf("topics buttons = %v", got)
	}
	if text := f.tg.Text(900); !strings.Contains(text, "Current: 30 days") {
		t.Fatalf("topics text = %s", text)
	}
	pressPanel(f, t, 900, dataGrid(domain.OptionDeleteAfterDays, 0))
	buttons := f.tg.Buttons(900)
	if got := texts(buttons); strings.Join(got, "|") != "Off|7d|14d|[30d]|60d|90d|‹ Back" {
		t.Fatalf("days row = %v", got)
	}
	if buttons[0].Row != 1 || buttons[5].Row != 1 || buttons[6].Data != dataGroup(groupIndex(domain.GroupTopics)) {
		t.Fatalf("days rows = %+v", buttons)
	}
	if text := f.tg.Text(900); !strings.Contains(text, "<b>Delete closed topics after</b>") || strings.Contains(text, panelOnlyPackIcons) || !strings.Contains(text, "Current: 30 days") {
		t.Fatalf("days text = %s", text)
	}
	pressPanel(f, t, 900, dataPick(domain.OptionDeleteAfterDays, 0))
	if v := f.opts.Get().String(domain.OptionDeleteAfterDays); v != "0" || f.opts.DeleteAfter() != 0 {
		t.Fatalf("after Off: value %q", v)
	}
	if got := texts(f.tg.Buttons(900)); got[0] != "Off Delete closed topics after" {
		t.Fatalf("group after Off = %v", got)
	}
	pressPanel(f, t, 900, dataGrid(domain.OptionDeleteAfterDays, 0))
	if got := texts(f.tg.Buttons(900)); got[0] != "[Off]" {
		t.Fatalf("days row after Off = %v", got)
	}
	pressPanel(f, t, 900, dataPick(domain.OptionDeleteAfterDays, 1))
	if f.opts.DeleteAfter() != 7*24*time.Hour {
		t.Fatalf("after 7d: %v", f.opts.DeleteAfter())
	}

	// A hand-edited value outside the list shows without a bracket.
	if err := f.opts.Set(f.ctx, domain.OptionDeleteAfterDays, "45", 1); err != nil {
		t.Fatal(err)
	}
	pressPanel(f, t, 900, dataGrid(domain.OptionDeleteAfterDays, 0))
	if got := texts(f.tg.Buttons(900)); strings.Join(got, "|") != "Off|7d|14d|30d|60d|90d|‹ Back" || !strings.Contains(f.tg.Text(900), "Current: 45 days") {
		t.Fatalf("days row with 45 = %v / %s", got, f.tg.Text(900))
	}
	pressPanel(f, t, 900, dataClose())
	if text := f.tg.Text(900); !strings.Contains(text, "Delete closed topics after: 45 days") || !strings.Contains(text, "Redact secrets: off") {
		t.Fatalf("summary = %s", text)
	}
}

func TestPanelQuietGroup(t *testing.T) {
	f := newBridgeFixture(t)
	pressPanel(f, t, 900, dataGroup(groupIndex(domain.GroupQuiet)))
	got := texts(f.tg.Buttons(900))
	want := "☑ Quiet while at the desk|3 min Away after|☑ Hold topic edits|Silent Screen posts|☑ Re-announce on leaving|↺ Reset to defaults|‹ Back|✖ Close"
	if strings.Join(got, "|") != want {
		t.Fatalf("quiet buttons = %v", got)
	}

	pressPanel(f, t, 900, dataGrid(domain.OptionQuietIdleMinutes, 0))
	buttons := f.tg.Buttons(900)
	if got := texts(buttons); strings.Join(got, "|") != "1m|2m|[3m]|5m|10m|15m|‹ Back" {
		t.Fatalf("minutes grid = %v", got)
	}
	if buttons[0].Row != 1 || buttons[5].Row != 1 || buttons[6].Data != dataGroup(groupIndex(domain.GroupQuiet)) {
		t.Errorf("minutes grid layout: rows %d/%d back %q", buttons[0].Row, buttons[5].Row, buttons[6].Data)
	}
	pressPanel(f, t, 900, dataPick(domain.OptionQuietIdleMinutes, 0))
	if f.opts.Get().QuietIdle() != time.Minute || f.options.Saved() != 1 {
		t.Fatalf("pick 1m: idle=%v saves=%d", f.opts.Get().QuietIdle(), f.options.Saved())
	}

	pressPanel(f, t, 900, dataGrid(domain.OptionQuietPosts, 0))
	if got := texts(f.tg.Buttons(900)); strings.Join(got, "|") != "[Silent]|Held|Normal|‹ Back" {
		t.Fatalf("posts grid = %v", got)
	}
	pressPanel(f, t, 900, dataPick(domain.OptionQuietPosts, 1))
	if f.opts.Get().QuietPosts() != domain.PostsHeld {
		t.Fatalf("pick held: posts=%q", f.opts.Get().QuietPosts())
	}
	pressPanel(f, t, 900, dataToggle(domain.OptionQuietEnabled))
	if f.opts.Get().QuietEnabled() {
		t.Error("toggle did not switch quiet off")
	}
	if got := texts(f.tg.Buttons(900)); got[0] != "☐ Quiet while at the desk" || got[1] != "1 min Away after" || got[3] != "Held Screen posts" {
		t.Errorf("quiet buttons after edits = %v", got)
	}
}
