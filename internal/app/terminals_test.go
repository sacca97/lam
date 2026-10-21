package app

import "testing"

func TestHideOwnedTerminals(t *testing.T) {
	list := []sessionInfo{
		{Name: "claude-proj"}, {Name: "term-claude-proj-1a2b3c4d"},
		{Name: "term-gone-1a2b3c4d"}, {Name: "term-notaterminal"},
	}
	got := hideOwnedTerminals(list)
	want := []string{"claude-proj", "term-gone-1a2b3c4d", "term-notaterminal"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestOwnedTerminalsAreNotSharedBetweenCollidingNames(t *testing.T) {
	list := []sessionInfo{{Name: "foo"}, {Name: "term-foo-0123abcd"}, {Name: "bar"}, {Name: "term-bar-89abcdef"}}
	if got := ownedTerminals(list, "foo"); len(got) != 1 || got[0].Name != "term-foo-0123abcd" {
		t.Fatalf("ownedTerminals(foo) = %v", got)
	}
	// "Foo" and "foo" slug to the same label, so neither may claim the shell.
	list = append(list, sessionInfo{Name: "Foo"})
	if got := ownedTerminals(list, "foo"); got != nil {
		t.Fatalf("ambiguous label still claimed: %v", got)
	}
	if got := renamedTerminal("term-foo-0123abcd", "baz"); got != "term-baz-0123abcd" {
		t.Fatalf("renamedTerminal = %q", got)
	}
}
