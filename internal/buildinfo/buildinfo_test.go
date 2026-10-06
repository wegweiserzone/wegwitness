package buildinfo

import (
	"strings"
	"testing"
)

func TestGetFillsDefaults(t *testing.T) {
	got := Get()

	if got.Version == "" {
		t.Error("Version is empty; it should fall back to \"dev\"")
	}
	if got.GoVersion == "" {
		t.Error("GoVersion is empty")
	}
	if !strings.Contains(got.Platform, "/") {
		t.Errorf("Platform = %q, want GOOS/GOARCH", got.Platform)
	}
}

func TestInfoString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "without commit",
			info: Info{Version: "0.1.0", GoVersion: "go1.26.0", Platform: "linux/amd64"},
			want: "wegweiser 0.1.0 (go1.26.0, linux/amd64)",
		},
		{
			name: "commit is abbreviated",
			info: Info{
				Version:   "0.1.0",
				Commit:    "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser 0.1.0 (a1b2c3d, go1.26.0, linux/amd64)",
		},
		{
			name: "a dirty tree is visible",
			info: Info{
				Version:   "0.1.0",
				Commit:    "a1b2c3d4e5f6",
				Modified:  true,
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser 0.1.0 (a1b2c3d-dirty, go1.26.0, linux/amd64)",
		},
		{
			name: "a short commit is left alone",
			info: Info{Version: "dev", Commit: "abc", GoVersion: "go1.26.0", Platform: "darwin/arm64"},
			want: "wegweiser dev (abc, go1.26.0, darwin/arm64)",
		},
		{
			// git describe --always falls back to the abbreviated commit when
			// no tag exists, so printing both would repeat it.
			name: "an untagged build does not repeat the commit",
			info: Info{
				Version:   "56b82a0",
				Commit:    "56b82a0c1b435d1d09bdc17fafffe947d2059bbb",
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser 56b82a0 (go1.26.0, linux/amd64)",
		},
		{
			name: "an untagged dirty build says so once, not twice",
			info: Info{
				Version:   "56b82a0-dirty",
				Commit:    "56b82a0c1b435d1d09bdc17fafffe947d2059bbb",
				Modified:  true,
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser 56b82a0-dirty (go1.26.0, linux/amd64)",
		},
		{
			// No ldflags: the version falls back to "dev" and the commit comes
			// from the embedded VCS metadata, so both carry information.
			name: "a dev build reports the commit it was built from",
			info: Info{
				Version:   "dev",
				Commit:    "56b82a0c1b435d1d09bdc17fafffe947d2059bbb",
				Modified:  true,
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser dev (56b82a0-dirty, go1.26.0, linux/amd64)",
		},
		{
			name: "a tagged dirty build",
			info: Info{
				Version:   "v0.1.0-dirty",
				Commit:    "56b82a0c1b435d1d09bdc17fafffe947d2059bbb",
				Modified:  true,
				GoVersion: "go1.26.0",
				Platform:  "linux/amd64",
			},
			want: "wegweiser v0.1.0-dirty (56b82a0-dirty, go1.26.0, linux/amd64)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.info.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
