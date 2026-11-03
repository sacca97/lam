package app

import "testing"

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
