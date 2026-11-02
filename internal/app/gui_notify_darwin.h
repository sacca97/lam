//go:build gui && darwin

#ifndef LAM_NOTIFY_DARWIN_H
#define LAM_NOTIFY_DARWIN_H

// lamNotifyInit makes the app the notification centre's delegate and asks for
// permission; it returns 0 when the process is not an app bundle.
int lamNotifyInit(void);
// lamNotifySend shows a notification that reports a click by its identifier.
int lamNotifySend(const char *title, const char *body, const char *ident);

#endif
