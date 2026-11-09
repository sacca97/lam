package app

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSessionPhase(t *testing.T) {
	for _, c := range []struct {
		s    sessionInfo
		want sessionPhase
	}{
		{sessionInfo{}, stateIdle},
		{sessionInfo{Activity: activityQuiet}, stateIdle},
		{sessionInfo{Activity: activityActive}, stateWorking},
		{sessionInfo{Unread: true}, stateUnread},
		{sessionInfo{Unread: true, Activity: activityActive}, stateUnread}, // finished and working again: still waiting for you
	} {
		if got := c.s.Phase(); got != c.want {
			t.Errorf("%+v: phase %v, want %v", c.s, got, c.want)
		}
	}
	if (sessionInfo{Unread: true, Activity: activityActive}).Working() != true {
		t.Error("an unread session that works again is still working")
	}
}

// The strings other machines and scripts read must not change.
func TestSessionPhaseWireValues(t *testing.T) {
	if activityActive != "active" || activityQuiet != "quiet" {
		t.Fatal("the activity values are part of the remote protocol")
	}
	for p, want := range map[sessionPhase]string{stateUnread: "unread", stateWorking: "working", stateIdle: "idle", sessionPhase(99): "idle"} {
		if p.String() != want {
			t.Errorf("%d: %q, want %q", p, p.String(), want)
		}
	}
	if stateUnread.Label() != "Unread" || stateWorking.Label() != "Working" || stateIdle.Label() != "Idle" {
		t.Error("sidebar headings changed")
	}
}

func TestLsJSONCarriesState(t *testing.T) {
	var b bytes.Buffer
	err := writeLs(&b, []sessionInfo{{Name: "a", Activity: activityActive}, {Name: "b", Unread: true}, {Name: "c"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"working", "unread", "idle"} {
		if got[i]["state"] != want {
			t.Errorf("%v: state %v, want %s", got[i]["name"], got[i]["state"], want)
		}
		for _, old := range []string{"name", "cwd", "command", "unread", "shell"} {
			if _, ok := got[i][old]; !ok {
				t.Errorf("field %q went missing from lam ls --json", old)
			}
		}
	}
}
