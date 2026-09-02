package telegram_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestSendTextSinglePart(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	if err := h.gw.SendText(h.ctx, 42, `a < b & "c"`, false); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	f := calls[0].form
	if f.Get("text") != "a &lt; b &amp; &#34;c&#34;" {
		t.Errorf("text = %q", f.Get("text"))
	}
	if f.Get("chat_id") != "-1001234567890" || f.Get("message_thread_id") != "42" ||
		f.Get("parse_mode") != "HTML" || f.Get("disable_notification") != "true" {
		t.Errorf("form = %v", f)
	}
}

func TestSendTextCodeAndChunks(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	lines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		lines = append(lines, strings.Repeat("x", 30)+" <"+itoa(i)+">")
	}
	text := strings.Join(lines, "\n") // ~10.8k runes -> 3 parts
	if err := h.gw.SendText(h.ctx, 42, text, true); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMessage")
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	var joined []string
	for i, c := range calls {
		body := c.form.Get("text")
		if !strings.HasPrefix(body, "<pre>") || !strings.HasSuffix(body, "</pre>") {
			t.Errorf("part %d not wrapped in <pre>: %.20q", i+1, body)
		}
		if strings.Contains(body, "<0>") {
			t.Errorf("part %d contains unescaped angle brackets", i+1)
		}
		joined = append(joined, strings.TrimSuffix(strings.TrimPrefix(body, "<pre>"), "</pre>"))
	}
	if got := strings.Join(joined, "\n"); got != strings.ReplaceAll(strings.ReplaceAll(text, "<", "&lt;"), ">", "&gt;") {
		t.Error("parts do not reassemble into the escaped text")
	}
}

func TestSendTextStopsAtFirstFailure(t *testing.T) {
	h := newHarness(t)
	n := 0
	h.api.on("sendMessage", func(url.Values) apiReply {
		n++
		if n == 2 {
			return errReply(400, "Bad Request: message thread not found")
		}
		return okReply(map[string]any{"message_id": n})
	})
	text := strings.Repeat("a", 4032) + "\n" + strings.Repeat("b", 4032) + "\n" + strings.Repeat("c", 10)
	err := h.gw.SendText(h.ctx, 42, text, false)
	if !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("err = %v, want ErrTopicGone", err)
	}
	if !strings.Contains(err.Error(), "thread 42 part 2/3") {
		t.Errorf("err = %v, want thread and part in message", err)
	}
	if got := len(h.api.callsOf("sendMessage")); got != 2 {
		t.Errorf("calls = %d, want 2 (third part must not be sent)", got)
	}
}

func TestSendTextEmptyIsNoop(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.SendText(h.ctx, 42, "", false); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.methods()); n != 0 {
		t.Errorf("%d calls for empty text", n)
	}
}
