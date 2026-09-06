package telegram_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestGatewayDownload(t *testing.T) {
	h := newHarness(t)
	h.api.on("getFile", func(form url.Values) apiReply {
		if form.Get("file_id") != "f1" {
			return errReply(400, "Bad Request: wrong file id")
		}
		return okReply(map[string]any{"file_id": "f1", "file_unique_id": "u1", "file_size": 5, "file_path": "photos/1.jpg"})
	})
	h.api.serveFile("photos/1.jpg", []byte("hello"), 200)
	data, err := h.gw.Download(h.ctx, "f1", 1<<20)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Download = %q, %v", data, err)
	}
	if n := h.api.fileRequests(); n != 1 {
		t.Fatalf("file requests = %d", n)
	}
	if strings.Contains(h.buf.String(), testSecret) {
		t.Fatal("token leaked into the log")
	}
}

func TestGatewayDownloadTooBig(t *testing.T) {
	h := newHarness(t)
	h.api.on("getFile", func(url.Values) apiReply {
		return okReply(map[string]any{"file_id": "f1", "file_unique_id": "u1", "file_size": 5000, "file_path": "docs/big.bin"})
	})
	h.api.serveFile("docs/big.bin", []byte(strings.Repeat("x", 5000)), 200)
	// Refused by the reported size, before any GET.
	if _, err := h.gw.Download(h.ctx, "f1", 100); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("by size: %v", err)
	}
	if n := h.api.fileRequests(); n != 0 {
		t.Fatalf("file fetched despite the size: %d", n)
	}
	// Refused by the body when getFile under-reports the size.
	h.api.on("getFile", func(url.Values) apiReply {
		return okReply(map[string]any{"file_id": "f1", "file_unique_id": "u1", "file_size": 0, "file_path": "docs/big.bin"})
	})
	if _, err := h.gw.Download(h.ctx, "f1", 100); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("by body: %v", err)
	}
	// Telegram's own refusal.
	h.api.on("getFile", func(url.Values) apiReply { return errReply(400, "Bad Request: file is too big") })
	if _, err := h.gw.Download(h.ctx, "f1", 1<<20); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("by telegram: %v", err)
	}
}

func TestGatewayDownloadHTTPError(t *testing.T) {
	h := newHarness(t)
	h.api.on("getFile", func(url.Values) apiReply {
		return okReply(map[string]any{"file_id": "f1", "file_unique_id": "u1", "file_size": 3, "file_path": "gone/1.jpg"})
	})
	h.api.serveFile("gone/1.jpg", nil, 500)
	_, err := h.gw.Download(h.ctx, "f1", 1<<20)
	if err == nil || !strings.Contains(err.Error(), "status 500") || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err = %v", err)
	}
	if _, err := h.gw.Download(h.ctx, "f1", 1<<20); err == nil {
		t.Fatal("second call succeeded")
	}
	if _, err := h.gw.Download(h.ctx, "missing", 1<<20); err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("getFile error = %v", err)
	}
}
