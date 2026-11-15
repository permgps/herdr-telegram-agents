package telegram_test

import (
	"net/url"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestOwnerAmbiguousCreatesAreNotReplayed(t *testing.T) {
	for _, method := range []string{"createForumTopic", "sendMessage", "sendDocument"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			h.api.on(method, func(url.Values) apiReply { return okReply("accepted but invalid response") })
			var err error
			switch method {
			case "createForumTopic":
				_, err = h.gw.CreateTopic(h.ctx, "test", domain.StatusWorking)
			case "sendMessage":
				_, err = h.gw.Send(h.ctx, domain.Outgoing{Text: "hello"})
			case "sendDocument":
				err = h.gw.SendDocument(h.ctx, domain.Document{Name: "a.txt", Data: []byte("x")})
			}
			if err == nil || len(h.api.callsOf(method)) != 1 {
				t.Fatalf("%s: err=%v calls=%d", method, err, len(h.api.callsOf(method)))
			}
		})
	}
}
