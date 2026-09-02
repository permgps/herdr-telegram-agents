package system

import (
	"context"
	"strings"
	"testing"
)

func TestOpenCommand(t *testing.T) {
	tests := []struct {
		goos, name string
		args       []string
	}{
		{"darwin", "open", []string{"https://t.me/x?start=setup"}},
		{"linux", "xdg-open", []string{"https://t.me/x?start=setup"}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", "https://t.me/x?start=setup"}},
	}
	for _, tt := range tests {
		name, args, err := openCommand(tt.goos, "https://t.me/x?start=setup")
		if err != nil || name != tt.name || strings.Join(args, " ") != strings.Join(tt.args, " ") {
			t.Fatalf("%s: %q %v %v", tt.goos, name, args, err)
		}
	}
	if _, _, err := openCommand("darwin", "file:///etc/passwd"); err == nil {
		t.Fatal("non-http link should be rejected")
	}
	if err := OpenURL(context.Background(), "ftp://x"); err == nil {
		t.Fatal("OpenURL should reject non-http links")
	}
}
