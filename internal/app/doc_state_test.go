package app

import "testing"

func TestDocTransitions(t *testing.T) {
	allowed := map[[2]docState]bool{
		{docLoading, docReady}: true, {docLoading, docFailed}: true, {docLoading, docClosed}: true,
		{docReady, docLoading}: true, {docReady, docClosed}: true,
		{docFailed, docLoading}: true, {docFailed, docClosed}: true,
	}
	for from := docLoading; from <= docClosed; from++ {
		for to := docLoading; to <= docClosed; to++ {
			if got := docMayMove(from, to); got != allowed[[2]docState{from, to}] {
				t.Errorf("%v → %v: allowed = %v", from, to, got)
			}
		}
	}
	if docMayMove(docClosed, docLoading) {
		t.Error("a closed tab must stay closed: a late answer from the loader would revive it")
	}
}
