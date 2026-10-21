package app

import "sync/atomic"

// dirtyFlag says the screen needs redrawing and wakes the draw loop. Waking it
// is what keeps echo latency low: a character that comes back from the
// session is drawn as it arrives, not at the next tick of a timer.
type dirtyFlag struct {
	v    atomic.Bool
	wake chan struct{}
}

func newDirtyFlag() *dirtyFlag { return &dirtyFlag{wake: make(chan struct{}, 1)} }

// Store sets or clears the flag. The draw loop is woken only when the flag
// goes from clean to dirty: while output keeps arriving the flag stays set
// until a frame is drawn, and waking the loop for every chunk would cost far
// more than the drawing it saves.
func (d *dirtyFlag) Store(dirty bool) {
	if !dirty {
		d.v.Store(false)
		return
	}
	if !d.v.Swap(true) {
		select {
		case d.wake <- struct{}{}:
		default: // already awake
		}
	}
}

func (d *dirtyFlag) Swap(dirty bool) bool { return d.v.Swap(dirty) }
func (d *dirtyFlag) Load() bool           { return d.v.Load() }
