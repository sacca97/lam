package app

// docState is where a document tab is in its life. It used to be three fields
// (loading, err, closed) that could contradict each other; now there is one,
// and the only way to change it, transition, refuses what is not allowed.
//
//	Loading ──(file read)──▶ Ready ──(reload)──▶ Loading
//	   │                       │
//	   └──(read failed)──▶ Failed ──(open again / reload)──▶ Loading
//
//	any state ──(tab closed)──▶ Closed   (final: a late answer from the
//	                                      loader is dropped)
//
// The loader goroutine and the UI both read it, under guiApp.mu.
type docState int

const (
	docLoading docState = iota // the file is being read and prepared
	docReady                   // drawn from dv.data
	docFailed                  // dv.err says why
	docClosed                  // the tab is gone
)

func (s docState) String() string {
	switch s {
	case docLoading:
		return "loading"
	case docReady:
		return "ready"
	case docFailed:
		return "failed"
	}
	return "closed"
}

// docTransitions lists, for each state, the ones it may move to.
var docTransitions = map[docState][]docState{
	docLoading: {docReady, docFailed, docClosed},
	docReady:   {docLoading, docClosed},
	docFailed:  {docLoading, docClosed},
	docClosed:  {},
}

// docMayMove reports whether a document in state from may move to state to.
func docMayMove(from, to docState) bool {
	for _, ok := range docTransitions[from] {
		if ok == to {
			return true
		}
	}
	return false
}
