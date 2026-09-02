package system_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
)

func lookupFrom(m map[string]string) system.Lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func home() (string, error) { return "/home/u", nil }

func TestReadEnvFromFullSet(t *testing.T) {
	env, err := system.ReadEnvFrom(lookupFrom(map[string]string{
		"HERDR_PLUGIN_ID":         "x.y",
		"HERDR_PLUGIN_ROOT":       "/root",
		"HERDR_PLUGIN_CONFIG_DIR": "/cfg",
		"HERDR_PLUGIN_STATE_DIR":  "/state",
		"HERDR_SOCKET_PATH":       "/tmp/h.sock",
		"HERDR_BIN_PATH":          "/usr/local/bin/herdr",
		"HERDR_ENV":               "1",
		"LOG_LEVEL":               "debug",
		"PATH":                    "/usr/bin",
	}), home)
	if err != nil {
		t.Fatal(err)
	}
	want := system.PluginEnv{
		PluginID: "x.y", Root: "/root", ConfigDir: "/cfg", StateDir: "/state",
		SocketPath: "/tmp/h.sock", BinPath: "/usr/local/bin/herdr", LogLevel: "debug", Path: "/usr/bin", InsideHerdr: true,
	}
	if env != want {
		t.Fatalf("env = %+v\nwant %+v", env, want)
	}
}

func TestReadEnvFromFallbacks(t *testing.T) {
	env, err := system.ReadEnvFrom(lookupFrom(map[string]string{
		"HERDR_PLUGIN_CONFIG_DIR": "/cfg",
		"HERDR_PLUGIN_STATE_DIR":  "/state",
	}), home)
	if err != nil {
		t.Fatal(err)
	}
	if env.PluginID != system.DefaultPluginID || env.BinPath != "herdr" || env.InsideHerdr || env.LogLevel != "" {
		t.Fatalf("fallbacks: %+v", env)
	}
	if want := filepath.Join("/home/u", ".config", "herdr", "herdr.sock"); env.SocketPath != want {
		t.Fatalf("SocketPath = %q, want %q", env.SocketPath, want)
	}
}

func TestReadEnvFromMissingDirs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no config dir", map[string]string{"HERDR_PLUGIN_STATE_DIR": "/s"}, "HERDR_PLUGIN_CONFIG_DIR"},
		{"no state dir", map[string]string{"HERDR_PLUGIN_CONFIG_DIR": "/c"}, "HERDR_PLUGIN_STATE_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := system.ReadEnvFrom(lookupFrom(tt.env), home)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want mention of %s", err, tt.want)
			}
		})
	}
}

func TestSocketPathWithoutHome(t *testing.T) {
	boom := errors.New("no home")
	_, err := system.SocketPath(lookupFrom(nil), func() (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}
