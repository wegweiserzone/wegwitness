package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

const secret = "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="

func TestLoad(t *testing.T) {
	t.Parallel()
	cfg, err := Load(write(t, "cluster:\n  advertise: \"192.0.2.9:8054\"\n  secret: \""+secret+"\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != DefaultListen || cfg.Dir != DefaultDir || cfg.Level != slog.LevelInfo || len(cfg.Secret) != 32 {
		t.Errorf("config = %+v, want the defaults and the secret decoded", cfg)
	}
}

func TestLoadRefuses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ body, want string }{
		"no address":    {"cluster:\n  secret: \"" + secret + "\"\n", "advertise"},
		"no secret":     {"cluster:\n  advertise: \"192.0.2.9:8054\"\n", "secret"},
		"a stray key":   {"cluster:\n  advertise: \"192.0.2.9:8054\"\n  secret: \"" + secret + "\"\n  peers: []\n", "peers"},
		"a bad address": {"cluster:\n  advertise: \"192.0.2.9\"\n  secret: \"" + secret + "\"\n", "host:port"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load = %v, want it refused, naming %q", err, tc.want)
			}
		})
	}
}
