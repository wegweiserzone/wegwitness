package main

import "testing"

// An address that means every interface is asked on loopback; one that names
// an interface is asked there.
func TestLocal(t *testing.T) {
	t.Parallel()
	for listen, want := range map[string]string{
		":8054":            "127.0.0.1:8054",
		"0.0.0.0:8054":     "127.0.0.1:8054",
		"[::]:8054":        "127.0.0.1:8054",
		"192.0.2.9:8054":   "192.0.2.9:8054",
		"[2001:db8::]:854": "[2001:db8::]:854",
	} {
		if got := local(listen); got != want {
			t.Errorf("local(%q) = %q, want %q", listen, got, want)
		}
	}
}
