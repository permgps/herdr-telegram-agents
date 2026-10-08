package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The install scripts read scripts/signing/allowed_signers, the updater the
// compiled-in releaseSigners; both must name exactly the same keys.
func TestReleaseSignersMatchScripts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "signing", "allowed_signers"))
	if err != nil {
		t.Fatal(err)
	}
	var fromFile []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != releaseNamespace || fields[1] != `namespaces="`+releaseNamespace+`"` {
			t.Fatalf("allowed_signers line %q: want principal and namespaces=%q", line, releaseNamespace)
		}
		fromFile = append(fromFile, fields[2]+" "+fields[3])
	}
	var compiled []string
	for _, line := range strings.Split(releaseSigners, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			compiled = append(compiled, line)
		}
	}
	if strings.Join(fromFile, "\n") != strings.Join(compiled, "\n") {
		t.Fatalf("allowed_signers keys %q differ from releaseSigners %q", fromFile, compiled)
	}
	keys, err := releaseKeys()
	if err != nil || len(keys) != len(compiled) {
		t.Fatalf("releaseKeys = %d, %v; want %d", len(keys), err, len(compiled))
	}
}
