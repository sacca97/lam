//go:build gui && darwin

package app

/*
#cgo LDFLAGS: -framework AppKit -framework UserNotifications
#include <stdlib.h>
#include "gui_notify_darwin.h"
*/
import "C"

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Notifications go through the system's own notification centre as this app, so
// a click comes back here: osascript's belong to Script Editor and cannot.
type pendingClick struct {
	fn func()
	at time.Time
}

var (
	notifyClicks sync.Map // identifier -> pendingClick
	notifyID     atomic.Int64
	notifyOnce   sync.Once
	notifyReady  atomic.Bool
)

func init() { notifyNative = nativeNotify }

// startNativeNotifications is run once the window exists.
func startNativeNotifications() {
	notifyOnce.Do(func() {
		status := C.lamNotifyInit()
		notifyReady.Store(status == 1)
		if status != 1 {
			logEvent("notifications: native centre not started (status %d); using osascript, whose clicks cannot reach lam", int(status))
		}
	})
}

//export lamNotifyLog
func lamNotifyLog(line *C.char) { logEvent("notifications: %s", C.GoString(line)) }

//export lamNotificationClicked
func lamNotificationClicked(ident *C.char) {
	if v, ok := notifyClicks.LoadAndDelete(C.GoString(ident)); ok {
		go v.(pendingClick).fn()
	}
}

func nativeNotify(title, body string, onClick func()) bool {
	if !notifyReady.Load() {
		return false
	}
	id := "lam-" + strconv.FormatInt(notifyID.Add(1), 10)
	if onClick != nil {
		now := time.Now()
		notifyClicks.Range(func(k, v any) bool { // forget what was never clicked
			if now.Sub(v.(pendingClick).at) > 12*time.Hour {
				notifyClicks.Delete(k)
			}
			return true
		})
		notifyClicks.Store(id, pendingClick{fn: onClick, at: now})
	}
	ct, cb, ci := C.CString(title), C.CString(body), C.CString(id)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(ci))
	if C.lamNotifySend(ct, cb, ci) != 1 {
		notifyClicks.Delete(id)
		return false
	}
	return true
}
