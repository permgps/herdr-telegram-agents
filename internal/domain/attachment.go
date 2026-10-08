package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// safeNameMaxBytes bounds an inbox file name derived from what the sender
// called the file. File systems limit a name in bytes (ext4's NAME_MAX is
// 255), so the cap is in bytes too. The longest name built around it is a
// private one: "shared-" + a 20-digit actor id + "-" + a 26-char rand.Text()
// + "-" is 55 bytes of prefix, a collision suffix "-NNN" adds 4, and the
// temp file ".<name>.<10 digits>.tmp" another 16, so 120 + 55 + 4 + 16 = 195.
const safeNameMaxBytes = 120

// safeExtMaxBytes is the longest suffix, dot included, kept as the
// extension when a long name is cut.
const safeExtMaxBytes = 16

// SafeFileName reduces a sender-provided file name to letters, digits,
// dots, hyphens and underscores: every other rune becomes a hyphen, runs of
// hyphens collapse, leading and trailing dots and hyphens go, and the
// result is cut on a rune boundary to safeNameMaxBytes. A cut keeps the
// extension (the last dot and at most safeExtMaxBytes) and shortens the
// stem. An empty result yields fallback.
func SafeFileName(name, fallback string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r == '.' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > safeNameMaxBytes {
		stem, ext := out, ""
		if dot := strings.LastIndexByte(out, '.'); dot > 0 && len(out)-dot <= safeExtMaxBytes {
			stem, ext = out[:dot], out[dot:]
		}
		out = strings.Trim(cutBytes(stem, safeNameMaxBytes-len(ext)), "-.") + ext
	}
	if out == "" {
		return fallback
	}
	return out
}

// cutBytes returns the longest prefix of s of at most n bytes that ends on
// a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// InboxFileName is the file name an attachment gets in the inbox:
// <yyyymmdd-hhmmss>-<message id>-<safe name>.
func InboxFileName(at time.Time, messageID int, name string) string {
	return at.Format("20060102-150405") + "-" + strconv.Itoa(messageID) + "-" + name
}

// attachmentExtensions maps a declared MIME type to the extension used
// when the sender gave no file name.
var attachmentExtensions = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"audio/ogg":       ".ogg",
	"audio/mpeg":      ".mp3",
	"audio/mp4":       ".m4a",
	"audio/x-m4a":     ".m4a",
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
	"application/pdf": ".pdf",
	"text/plain":      ".txt",
}

// DefaultAttachmentName is the file name for an attachment whose sender
// gave none: the kind plus an extension from the MIME type, or the kind's
// usual one (photo.jpg, voice.ogg, audio.mp3, video.mp4, file.bin).
func DefaultAttachmentName(kind AttachmentKind, mime string) string {
	base, ext := "file", ".bin"
	switch kind {
	case AttachmentPhoto:
		base, ext = "photo", ".jpg"
	case AttachmentVoice:
		base, ext = "voice", ".ogg"
	case AttachmentAudio:
		base, ext = "audio", ".mp3"
	case AttachmentVideo:
		base, ext = "video", ".mp4"
	}
	if e, ok := attachmentExtensions[strings.ToLower(strings.TrimSpace(mime))]; ok {
		ext = e
	}
	return base + ext
}

// AttachmentPrompt is the text the agent receives for saved attachments:
// the caption, a blank line when there is one, then one absolute path per
// line.
func AttachmentPrompt(caption string, paths []string) string {
	caption = strings.TrimSpace(caption)
	lines := strings.Join(paths, "\n")
	if caption == "" {
		return lines
	}
	return caption + "\n\n" + lines
}
