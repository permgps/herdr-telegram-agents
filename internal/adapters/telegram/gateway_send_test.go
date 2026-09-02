package telegram_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestSendSinglePart(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: `a < b & "c"`}); err != nil {
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
	if f.Has("reply_parameters") {
		t.Errorf("reply_parameters sent without ReplyTo: %v", f)
	}
}

func TestSendToGeneralOmitsThread(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 0, Text: "started"}); err != nil {
		t.Fatal(err)
	}
	f := h.api.callsOf("sendMessage")[0].form
	if f.Has("message_thread_id") {
		t.Errorf("message_thread_id must be omitted for General: %v", f)
	}
}

func TestSendNotifyAndReply(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	text := strings.Repeat("a", 4032) + "\n" + strings.Repeat("b", 10)
	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: text, ReplyTo: 77, Notify: true}); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMessage")
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	first, second := calls[0].form, calls[1].form
	if first.Has("disable_notification") || second.Has("disable_notification") {
		t.Errorf("Notify must not disable notification: %v / %v", first, second)
	}
	rp := first.Get("reply_parameters")
	if !strings.Contains(rp, `"message_id":77`) || !strings.Contains(rp, `"allow_sending_without_reply":true`) {
		t.Errorf("first part reply_parameters = %q", rp)
	}
	if second.Has("reply_parameters") {
		t.Errorf("second part must not quote the message: %v", second)
	}
}

func TestSendHTMLPassesThrough(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })
	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 0, Text: `<a href="https://t.me/c/1/2">a &amp; b</a>`, HTML: true}); err != nil {
		t.Fatal(err)
	}
	if got := h.api.callsOf("sendMessage")[0].form.Get("text"); got != `<a href="https://t.me/c/1/2">a &amp; b</a>` {
		t.Errorf("text = %q", got)
	}
}

func TestSendCodeAndChunks(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 1}) })

	lines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		lines = append(lines, strings.Repeat("x", 30)+" <"+itoa(i)+">")
	}
	text := strings.Join(lines, "\n") // ~10.8k runes -> 3 parts
	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: text, Code: true}); err != nil {
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

func TestSendStopsAtFirstFailure(t *testing.T) {
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
	err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: text})
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

func TestSendClosedTopic(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return errReply(400, "Bad Request: TOPIC_CLOSED") })
	err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: "hi"})
	if !errors.Is(err, domain.ErrTopicClosed) {
		t.Fatalf("err = %v, want ErrTopicClosed", err)
	}
}

func TestSendEmptyIsNoop(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42}); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.methods()); n != 0 {
		t.Errorf("%d calls for empty text", n)
	}
}

func TestReact(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.React(h.ctx, 42, 77, "👍"); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("setMessageReaction")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	f := calls[0].form
	if f.Get("chat_id") != "-1001234567890" || f.Get("message_id") != "77" {
		t.Errorf("form = %v", f)
	}
	if r := f.Get("reaction"); !strings.Contains(r, `"type":"emoji"`) || !strings.Contains(r, `"emoji":"👍"`) {
		t.Errorf("reaction = %q", r)
	}
	if f.Has("message_thread_id") {
		t.Errorf("reactions carry no thread id: %v", f)
	}
}

func TestReactFailureIsTranslated(t *testing.T) {
	h := newHarness(t)
	h.api.on("setMessageReaction", func(url.Values) apiReply { return errReply(400, "Bad Request: REACTION_INVALID") })
	err := h.gw.React(h.ctx, 42, 77, "🙃")
	if err == nil || errors.Is(err, domain.ErrTopicGone) || errors.Is(err, domain.ErrTopicClosed) {
		t.Fatalf("err = %v, want a plain API error", err)
	}
	if !strings.Contains(err.Error(), "setMessageReaction") {
		t.Errorf("err = %v, want the method name", err)
	}
}
