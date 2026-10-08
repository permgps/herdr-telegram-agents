package app

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestDeliverText(t *testing.T) {
	keyFail := errors.New("send_keys failed")
	textFail := errors.New("send_text failed")
	tests := []struct {
		name      string
		text      string
		fail      map[string]error
		wantTyped bool
		wantErr   error
		wantTexts []testkit.TextCall
		wantKeys  []testkit.KeysCall
	}{
		{name: "prompt accepted", text: "fix the tests"},
		{
			name: "other prompt error is returned as is", text: "fix the tests",
			fail:    map[string]error{"prompt": domain.ErrAgentBusy},
			wantErr: domain.ErrAgentBusy,
		},
		{
			name: "blocked prompt is typed and submitted", text: "use the staging db",
			fail:      map[string]error{"prompt": domain.ErrAgentBlocked},
			wantTyped: true,
			wantTexts: []testkit.TextCall{{Target: "w1:p1", Text: "use the staging db"}},
			wantKeys:  []testkit.KeysCall{{Target: "w1:p1", Keys: []string{domain.KeyEnter}}},
		},
		{
			name: "line breaks are joined", text: "first line\r\n\nsecond line\n",
			fail:      map[string]error{"prompt": domain.ErrAgentBlocked},
			wantTyped: true,
			wantTexts: []testkit.TextCall{{Target: "w1:p1", Text: "first line second line"}},
			wantKeys:  []testkit.KeysCall{{Target: "w1:p1", Keys: []string{domain.KeyEnter}}},
		},
		{
			name: "nothing left to type", text: " \n ",
			fail:    map[string]error{"prompt": domain.ErrAgentBlocked},
			wantErr: domain.ErrAgentBlocked,
		},
		{
			name: "send_text failure sends no enter", text: "yes",
			fail:      map[string]error{"prompt": domain.ErrAgentBlocked, "text": textFail},
			wantErr:   textFail,
			wantTexts: []testkit.TextCall{{Target: "w1:p1", Text: "yes"}},
		},
		{
			name: "enter failure leaves the text typed", text: "yes",
			fail:      map[string]error{"prompt": domain.ErrAgentBlocked, "keys": keyFail},
			wantTyped: true,
			wantErr:   keyFail,
			wantTexts: []testkit.TextCall{{Target: "w1:p1", Text: "yes"}},
			wantKeys:  []testkit.KeysCall{{Target: "w1:p1", Keys: []string{domain.KeyEnter}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testkit.NewFakeHerdr(nil)
			for method, err := range tt.fail {
				h.FailNext(method, err)
			}
			typed, err := deliverText(context.Background(), h, slog.New(slog.DiscardHandler), "w1:p1", tt.text)
			if typed != tt.wantTyped {
				t.Fatalf("typed = %v, want %v", typed, tt.wantTyped)
			}
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := h.Prompts(); !reflect.DeepEqual(got, []string{"w1:p1: " + tt.text}) {
				t.Fatalf("Prompts = %q, want the one prompt first", got)
			}
			if got := h.Texts(); !reflect.DeepEqual(got, tt.wantTexts) {
				t.Fatalf("Texts = %v, want %v", got, tt.wantTexts)
			}
			if got := h.Keys(); !reflect.DeepEqual(got, tt.wantKeys) {
				t.Fatalf("Keys = %v, want %v", got, tt.wantKeys)
			}
		})
	}
}
