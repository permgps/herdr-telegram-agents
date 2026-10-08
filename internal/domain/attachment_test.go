package domain_test

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		{strings.Repeat("a", 100) + ".txt", "file.bin", strings.Repeat("a", 100) + ".txt"},
		{strings.Repeat("a", 300) + ".txt", "file.bin", strings.Repeat("a", 116) + ".txt"},
		{strings.Repeat("a", 300), "file.bin", strings.Repeat("a", 120)},
		// The cut stem loses its trailing separators, the extension stays.
		{strings.Repeat("a", 115) + " (copy).tar", "file.bin", strings.Repeat("a", 115) + ".tar"},
		// A last dot further back than 16 bytes is not an extension.
		{"x." + strings.Repeat("b", 200), "file.bin", "x." + strings.Repeat("b", 118)},
	}
	for _, tc := range cases {
		if got := domain.SafeFileName(tc.in, tc.fallback); got != tc.want {
			t.Errorf("SafeFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSafeFileNameCapsBytesOnRuneBoundary(t *testing.T) {
	got := domain.SafeFileName(strings.Repeat("文", 200)+".pdf", "file.bin")
	if len(got) > 120 || !utf8.ValidString(got) || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("SafeFileName(CJK) = %q (%d bytes), want <= 120 valid bytes ending .pdf", got, len(got))
	}
	// 116 bytes for the stem hold 38 three-byte runes.
	if want := strings.Repeat("文", 38) + ".pdf"; got != want {
		t.Fatalf("SafeFileName(CJK) = %q, want %q", got, want)
	}
}

// TestInboxNameFitsNameMax: the longest inbox name plus the longest private
// prefix and a collision suffix leaves room for the 16-byte temp-file
// decoration under ext4's 255-byte NAME_MAX.
func TestInboxNameFitsNameMax(t *testing.T) {
	name := domain.SafeFileName(strings.Repeat("文", 300)+".verylongext1234", "file.bin")
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	prefix := "shared-" + strconv.FormatInt(math.MinInt64, 10)[:20] + "-" + strings.Repeat("A", 26) + "-"
	full := prefix + domain.InboxFileName(at, math.MaxInt, name) + "-999"
	if len(full) >= 239 {
		t.Fatalf("worst-case name is %d bytes, want under 239", len(full))
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
