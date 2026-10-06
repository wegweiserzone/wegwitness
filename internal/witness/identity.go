package witness

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Prefix begins the identifier of every witness, and of nothing else
// (wegweiser's docs/decisions/d48-a-witness-is-known-by-its-identifier.md).
// wegweiser's internal/cluster holds the same constant.
const Prefix = "witness-"

// idFile is where a witness keeps its identifier, beside Raft's log.
const idFile = "id"

// Identity returns the identifier this witness is a member by, minting one the
// first time (wegweiser's docs/decisions/d42-membership-lives-in-the-log.md).
//
// configured is what the configuration file names, and may be empty. It has
// to begin with [Prefix], and once an identifier is kept the file is held to
// it: a member's identifier never changes.
func Identity(dir, configured string) (string, error) {
	if configured != "" && !strings.HasPrefix(configured, Prefix) {
		return "", fmt.Errorf("witness: the identifier %q has to begin %q, which is how the cluster "+
			"knows a witness (wegweiser's docs/decisions/d48-a-witness-is-known-by-its-identifier.md)",
			configured, Prefix)
	}
	path := filepath.Join(dir, idFile)
	held, err := os.ReadFile(path)
	switch {
	case err == nil:
		id := strings.TrimSpace(string(held))
		if configured != "" && configured != id {
			return "", fmt.Errorf("witness: this witness is member %q and its configuration names it %q; "+
				"a member's identifier never changes", id, configured)
		}
		return id, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("witness: read the identifier: %w", err)
	}

	id := configured
	if id == "" {
		b := make([]byte, 8)
		if _, rerr := rand.Read(b); rerr != nil {
			return "", fmt.Errorf("witness: mint an identifier: %w", rerr)
		}
		id = Prefix + hex.EncodeToString(b)
	}
	if merr := os.MkdirAll(dir, 0o700); merr != nil {
		return "", fmt.Errorf("witness: the directory for Raft's log: %w", merr)
	}
	if werr := os.WriteFile(path, []byte(id+"\n"), 0o600); werr != nil {
		return "", fmt.Errorf("witness: keep the identifier: %w", werr)
	}
	return id, nil
}

// IsWitness reports whether the member by this identifier is a witness.
func IsWitness(id string) bool { return strings.HasPrefix(id, Prefix) }
