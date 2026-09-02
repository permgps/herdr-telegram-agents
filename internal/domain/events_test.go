package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Compile-time assertions: every event type satisfies the marker interface.
var (
	_ domain.Event = domain.HerdrEvent{}
	_ domain.Event = domain.TopicMessage{}
	_ domain.Event = domain.TopicRenamed{}
	_ domain.Event = domain.TopicClosed{}
	_ domain.Event = domain.TopicReopened{}
	_ domain.Event = domain.RightsChanged{}
)

func TestChatMigratedErrorIs(t *testing.T) {
	var err error = &domain.ChatMigratedError{NewChatID: -1001234567890}
	if !errors.Is(err, domain.ErrChatMigrated) {
		t.Fatalf("errors.Is(ChatMigratedError, ErrChatMigrated) = false")
	}
	wrapped := fmt.Errorf("send text: %w", err)
	if !errors.Is(wrapped, domain.ErrChatMigrated) {
		t.Fatalf("wrapped ChatMigratedError does not match ErrChatMigrated")
	}
	var target *domain.ChatMigratedError
	if !errors.As(wrapped, &target) || target.NewChatID != -1001234567890 {
		t.Fatalf("errors.As lost the new chat id: %+v", target)
	}
	if errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("ChatMigratedError matched an unrelated sentinel")
	}
}

func TestTopicPatchEmpty(t *testing.T) {
	if !(domain.TopicPatch{}).Empty() {
		t.Fatalf("zero patch is not empty")
	}
	name := "x"
	if (domain.TopicPatch{Name: &name}).Empty() {
		t.Fatalf("patch with name reported empty")
	}
	st := domain.StatusDone
	if (domain.TopicPatch{Status: &st}).Empty() {
		t.Fatalf("patch with status reported empty")
	}
}
