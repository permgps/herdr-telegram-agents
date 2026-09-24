package state

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestUpdateStoreAtomicRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	s := NewUpdateStore(dir, nil)
	s.Now = func() time.Time { return now }
	job := domain.UpdateJob{ID: "job1", Phase: "installing", OldVersion: "1.0.0", TargetVersion: "1.1.0", ErrorMessage: strings.Repeat("x", 300) + "\nsecret"}
	if err := s.Save(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "update.json" {
		t.Fatalf("files=%v err=%v", files, err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got.Phase != "installing" || len(got.ErrorMessage) != 240 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	got, err = s.Recover(context.Background(), false)
	if err != nil || got.Phase != "interrupted" {
		t.Fatalf("recovery=%+v err=%v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "update.json"))
	if strings.Contains(string(data), "secret") {
		t.Fatal("unsanitized error persisted")
	}
}
