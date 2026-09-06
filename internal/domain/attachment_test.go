package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestSafeFileName(t *testing.T) {
	cases := []struct{ in, fallback, want string }{
		{"report.pdf", "file.bin", "report.pdf"},
		{"my photo (1).JPG", "photo.jpg", "my-photo-1-.JPG"},
		{"../../etc/passwd", "file.bin", "etc-passwd"},
		{"отчёт за неделю.txt", "file.bin", "отчёт-за-неделю.txt"},
		{"", "voice.ogg", "voice.ogg"},
		{"---", "file.bin", "file.bin"},
		{strings.Repeat("a", 100) + ".txt", "file.bin", strings.Repeat("a", 80)},
	}
	for _, tc := range cases {
		if got := domain.SafeFileName(tc.in, tc.fallback); got != tc.want {
			t.Errorf("SafeFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInboxFileName(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if got := domain.InboxFileName(at, 42, "photo.jpg"); got != "20260906-120000-42-photo.jpg" {
		t.Fatalf("InboxFileName = %q", got)
	}
}

func TestDefaultAttachmentName(t *testing.T) {
	cases := []struct {
		kind domain.AttachmentKind
		mime string
		want string
	}{
		{domain.AttachmentPhoto, "", "photo.jpg"},
		{domain.AttachmentPhoto, "image/png", "photo.png"},
		{domain.AttachmentVoice, "audio/ogg", "voice.ogg"},
		{domain.AttachmentAudio, "", "audio.mp3"},
		{domain.AttachmentVideo, "video/quicktime", "video.mov"},
		{domain.AttachmentDocument, "application/pdf", "file.pdf"},
		{domain.AttachmentDocument, "application/x-unknown", "file.bin"},
	}
	for _, tc := range cases {
		if got := domain.DefaultAttachmentName(tc.kind, tc.mime); got != tc.want {
			t.Errorf("DefaultAttachmentName(%s, %q) = %q, want %q", tc.kind, tc.mime, got, tc.want)
		}
	}
}

func TestAttachmentPrompt(t *testing.T) {
	if got := domain.AttachmentPrompt("look at this", []string{"/a/1.jpg"}); got != "look at this\n\n/a/1.jpg" {
		t.Fatalf("with caption = %q", got)
	}
	if got := domain.AttachmentPrompt("  ", []string{"/a/1.jpg"}); got != "/a/1.jpg" {
		t.Fatalf("without caption = %q", got)
	}
	if got := domain.AttachmentPrompt("three", []string{"/a/1", "/a/2", "/a/3"}); got != "three\n\n/a/1\n/a/2\n/a/3" {
		t.Fatalf("album = %q", got)
	}
}
