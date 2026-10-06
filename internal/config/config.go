// Package config reads the witness's configuration file.
//
// It holds what a witness needs before anything is running: the four things
// every member's cluster section holds
// (wegweiser's docs/decisions/d42-membership-lives-in-the-log.md), where to
// keep the log, and how loudly to report. Who the other members are is not in
// it: that is the cluster's log.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// Defaults for what the file leaves out.
const (
	DefaultListen = ":8054"
	DefaultDir    = "/var/lib/wegwitness"
)

// Config is the configuration a witness starts with.
type Config struct {
	// ID is the identifier the file names, empty when it names none and one
	// is to be minted.
	ID        string
	Listen    string
	Advertise string
	Secret    []byte
	Dir       string
	Level     slog.Level
}

type file struct {
	Cluster struct {
		ID        string `json:"id"`
		Listen    string `json:"listen"`
		Advertise string `json:"advertise"`
		Secret    string `json:"secret"`
	} `json:"cluster"`
	Dir string `json:"dir"`
	Log struct {
		Level string `json:"level"`
	} `json:"log"`
}

// Load reads the file at path. A key it does not know is refused rather than
// ignored, so that a misspelt one is not mistaken for a setting.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	var f file
	if uerr := yaml.UnmarshalStrict(raw, &f); uerr != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, uerr)
	}

	cfg := Config{
		ID:        strings.TrimSpace(f.Cluster.ID),
		Listen:    f.Cluster.Listen,
		Advertise: f.Cluster.Advertise,
		Dir:       f.Dir,
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Dir == "" {
		cfg.Dir = DefaultDir
	}
	if cfg.Advertise == "" {
		return Config{}, errors.New("config: cluster.advertise is where the other members reach this " +
			"witness, and is required: the address listened on may be every interface")
	}
	if _, _, serr := net.SplitHostPort(cfg.Advertise); serr != nil {
		return Config{}, fmt.Errorf("config: cluster.advertise has to be host:port: %w", serr)
	}
	secret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(f.Cluster.Secret))
	if err != nil || len(secret) == 0 {
		return Config{}, errors.New("config: cluster.secret has to be the cluster's shared secret, " +
			"base64, the same as on every member")
	}
	cfg.Secret = secret
	if lerr := cfg.Level.UnmarshalText([]byte(orDefault(f.Log.Level, "info"))); lerr != nil {
		return Config{}, fmt.Errorf("config: log.level: %w", lerr)
	}
	return cfg, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
