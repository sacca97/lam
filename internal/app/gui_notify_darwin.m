//go:build gui && darwin

#import <AppKit/AppKit.h>
#import <UserNotifications/UserNotifications.h>
#include "_cgo_export.h"
#include "gui_notify_darwin.h"

// A click on a notification brings the app to the front, even from the Dock
// being hidden or the window minimised, and tells Go which notification it was.
@interface LamNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation LamNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
        willPresentNotification:(UNNotification *)notification
          withCompletionHandler:(void (^)(UNNotificationPresentationOptions))done {
	done(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionSound);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
 didReceiveNotificationResponse:(UNNotificationResponse *)response
          withCompletionHandler:(void (^)(void))done {
	NSString *ident = response.notification.request.identifier;
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp activateIgnoringOtherApps:YES];
		for (NSWindow *w in NSApp.windows) {
			if (w.isMiniaturized) [w deminiaturize:nil];
		}
		NSWindow *front = NSApp.mainWindow ?: NSApp.windows.firstObject;
		[front makeKeyAndOrderFront:nil];
	});
	lamNotificationClicked((char *)ident.UTF8String);
	done();
}
@end

static LamNotifyDelegate *lamDelegate;

int lamNotifyInit(void) {
	if ([NSBundle mainBundle].bundleIdentifier == nil) return 0; // not run from lam.app
	@try {
		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		lamDelegate = [LamNotifyDelegate new];
		center.delegate = lamDelegate;
		[center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
		                      completionHandler:^(BOOL granted, NSError *error) {
			NSString *line = [NSString stringWithFormat:@"authorization granted=%d error=%@", granted, error ? error.localizedDescription : @"none"];
			lamNotifyLog((char *)line.UTF8String);
		}];
		return 1;
	} @catch (NSException *e) {
		lamDelegate = nil;
		NSString *line = [NSString stringWithFormat:@"notification centre unavailable: %@ %@", e.name, e.reason];
		lamNotifyLog((char *)line.UTF8String);
		return -1;
	}
}

int lamNotifySend(const char *title, const char *body, const char *ident) {
	if (lamDelegate == nil) return 0;
	@try {
		UNMutableNotificationContent *content = [UNMutableNotificationContent new];
		content.title = [NSString stringWithUTF8String:title];
		content.body = [NSString stringWithUTF8String:body];
		content.sound = [UNNotificationSound defaultSound];
		UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:[NSString stringWithUTF8String:ident]
		                                                                      content:content
		                                                                      trigger:nil];
		[[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:request withCompletionHandler:^(NSError *error) {
			if (error) {
				NSString *line = [NSString stringWithFormat:@"could not show a notification: %@", error.localizedDescription];
				lamNotifyLog((char *)line.UTF8String);
			}
		}];
		return 1;
	} @catch (NSException *e) {
		return 0;
	}
}
