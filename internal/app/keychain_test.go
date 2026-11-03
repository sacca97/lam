package app

import (
	"os"
	"testing"
)

func TestSecurityQuote(t *testing.T) {
	cases := map[string]string{
		`plain`:      `"plain"`,
		`has "quote`: `"has \"quote"`,
		`back\slash`: `"back\\slash"`,
		"two\nlines": `"two lines"`,
		`a b`:        `"a b"`,
	}
	for in, want := range cases {
		if got := securityQuote(in); got != want {
			t.Errorf("securityQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestVaultIndexKeepsNamesNotPasswords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := vaultAccounts(); len(got) != 0 {
		t.Fatalf("accounts = %v", got)
	}
	vaultIndexAdd("me@b")
	vaultIndexAdd("me@a")
	vaultIndexAdd("me@a") // once
	got := vaultAccounts()
	if len(got) != 2 || got[0] != "me@a" || got[1] != "me@b" {
		t.Fatalf("accounts = %v", got)
	}
	vaultIndexRemove("me@a")
	if got := vaultAccounts(); len(got) != 1 || got[0] != "me@b" {
		t.Fatalf("accounts = %v", got)
	}
	if st, err := os.Stat(vaultIndexPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("index file: %v %v", st, err)
	}
}
