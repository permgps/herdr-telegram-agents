package app

import (
	"context"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type fakeUpdateNotifier struct {
	count int
	id    int
	text  string
}

func (n *fakeUpdateNotifier) EditUpdate(_ context.Context, id int, text string) error {
	n.count++
	n.id = id
	n.text = text
	return nil
}

func TestUpdateResultEditsOneGeneralMessage(t *testing.T) {
	store := &memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "succeeded", TargetTag: "v1.2.0", PriorRunning: true, ChatID: 42, PanelMessageID: 100, NotificationStatus: "pending"}}
	n := &fakeUpdateNotifier{}
	if err := DeliverUpdateResult(context.Background(), store, n, 42, nil); err != nil {
		t.Fatal(err)
	}
	if err := DeliverUpdateResult(context.Background(), store, n, 42, nil); err != nil {
		t.Fatal(err)
	}
	if n.count != 1 || n.id != 100 || store.job.NotificationStatus != "sent" {
		t.Fatalf("notifier=%+v job=%+v", n, store.job)
	}
}
