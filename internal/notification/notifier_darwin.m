#import <UserNotifications/UserNotifications.h>
#import <AppKit/AppKit.h>

// Forward declarations of the Go callbacks (defined via //export in notifier_darwin.go)
extern void goNotificationCallback(char *accountId, char *folderId, char *threadId);
extern void goNotificationPermissionChanged(int status);

// Status codes returned to Go — must match
// darwinAuthorizationStatusToPermission in permission.go.
static const int kNotifStatusAuthorized = 0;
static const int kNotifStatusDenied = 1;
static const int kNotifStatusNotDetermined = 2;
static const int kNotifStatusUnknown = 3;

// Maps a UNNotificationSettings snapshot to a status code. Provisional
// and ephemeral authorization still deliver notifications, so they
// count as authorized.
static int statusFromSettings(UNNotificationSettings *settings) {
    switch (settings.authorizationStatus) {
        case UNAuthorizationStatusAuthorized:
        case UNAuthorizationStatusProvisional:
        case UNAuthorizationStatusEphemeral:
            return kNotifStatusAuthorized;
        case UNAuthorizationStatusDenied:
            return kNotifStatusDenied;
        case UNAuthorizationStatusNotDetermined:
            return kNotifStatusNotDetermined;
        default:
            return kNotifStatusUnknown;
    }
}

// Delegate that handles notification interactions and foreground presentation
@interface DaakNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation DaakNotificationDelegate

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
didReceiveNotificationResponse:(UNNotificationResponse *)response
         withCompletionHandler:(void (^)(void))completionHandler {
    NSDictionary *userInfo = response.notification.request.content.userInfo;
    NSString *accountId = userInfo[@"accountId"] ?: @"";
    NSString *folderId  = userInfo[@"folderId"]  ?: @"";
    NSString *threadId  = userInfo[@"threadId"]  ?: @"";

    goNotificationCallback(
        (char *)[accountId UTF8String],
        (char *)[folderId  UTF8String],
        (char *)[threadId  UTF8String]
    );

    completionHandler();
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
    // Show notification and play sound even when app is in foreground.
    // UNNotificationPresentationOptionBanner requires macOS 11.0+.
    UNNotificationPresentationOptions opts = UNNotificationPresentationOptionSound;
    if (@available(macOS 11.0, *)) {
        opts |= UNNotificationPresentationOptionBanner;
    }
    completionHandler(opts);
}

@end

static DaakNotificationDelegate *notifDelegate = nil;

// setupNotifications initializes UNUserNotificationCenter and requests authorization.
// Dispatches to the main queue since UNUserNotificationCenter delegate must be
// configured on the main thread for reliable callback delivery.
void setupNotifications(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        notifDelegate = [[DaakNotificationDelegate alloc] init];
        center.delegate = notifDelegate;

        [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                              completionHandler:^(BOOL granted, NSError *error) {
            if (error != nil) {
                NSLog(@"[Daak] Notification authorization error: %@", error);
                return;
            }
            NSLog(@"[Daak] Notification authorization granted: %d", granted);
            // Report the resulting status to Go so the app can surface a
            // denial (Settings row + one-time notice) instead of silently
            // dropping every later notification. The settings snapshot —
            // not the granted flag — decides: dismissing the prompt
            // without choosing leaves the status not-determined.
            [center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
                goNotificationPermissionChanged(statusFromSettings(settings));
            }];
        }];
    });
}

// showNotification creates and submits a notification asynchronously — never blocks.
void showNotification(const char *title, const char *body,
                      const char *accountId, const char *folderId, const char *threadId) {
    // Autorelease pool for the Go goroutine thread (which has no pool of its own).
    // The NSStrings are autoreleased by stringWithUTF8String:, and dispatch_async
    // retains them when copying the block to the heap, so they survive past the pool drain.
    @autoreleasepool {
    NSString *nsTitle     = [NSString stringWithUTF8String:title];
    NSString *nsBody      = [NSString stringWithUTF8String:body];
    NSString *nsAccountId = [NSString stringWithUTF8String:accountId];
    NSString *nsFolderId  = [NSString stringWithUTF8String:folderId];
    NSString *nsThreadId  = [NSString stringWithUTF8String:threadId];

    dispatch_async(dispatch_get_main_queue(), ^{
        UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
        content.title = nsTitle;
        content.body  = nsBody;
        content.sound = [UNNotificationSound defaultSound];
        content.userInfo = @{
            @"accountId": nsAccountId,
            @"folderId":  nsFolderId,
            @"threadId":  nsThreadId,
        };

        NSString *identifier = [[NSUUID UUID] UUIDString];
        UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:identifier
                                                                              content:content
                                                                              trigger:nil];

        [[UNUserNotificationCenter currentNotificationCenter]
            addNotificationRequest:request
             withCompletionHandler:^(NSError *error) {
                if (error != nil) {
                    NSLog(@"[Daak] Failed to deliver notification: %@", error);
                }
            }];
    });
    } // @autoreleasepool
}

// cancelNotifications removes all delivered notifications.
void cancelNotifications(void) {
    [[UNUserNotificationCenter currentNotificationCenter] removeAllDeliveredNotifications];
}

// getNotificationPermissionStatus returns the live authorization status
// as a kNotifStatus* code. getNotificationSettingsWithCompletionHandler
// is async, so this blocks the calling thread on a semaphore — bounded
// by a 2s timeout, after which it reports unknown rather than hanging.
// Callers must invoke it from a Go goroutine, never the macOS main
// thread and never on a hot path.
int getNotificationPermissionStatus(void) {
    __block int result = kNotifStatusUnknown;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [[UNUserNotificationCenter currentNotificationCenter]
        getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
            result = statusFromSettings(settings);
            dispatch_semaphore_signal(sem);
        }];
    dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC);
    if (dispatch_semaphore_wait(sem, timeout) != 0) {
        NSLog(@"[Daak] Notification settings query timed out, reporting unknown");
    }
    return result;
}

// openNotificationSettings opens the macOS Notifications settings pane,
// deep-linked to this app's own row via the ?id= bundle identifier so
// the user lands on Daak's toggle instead of the pane's top level.
// The URL form changed in macOS 13 (Ventura): System Settings panes
// moved from legacy com.apple.preference.* identifiers to
// com.apple.<Pane>-Settings.extension bundle IDs. The legacy form
// still opens the pane on macOS 12 and earlier (and reportedly still
// resolves on 13+), but the extension form is the current one, so the
// version picks the right form per OS.
void openNotificationSettings(void) {
    NSString *bundleID = [[NSBundle mainBundle] bundleIdentifier] ?: @"net.absons.daak";
    NSURL *url = nil;
    if (@available(macOS 13.0, *)) {
        NSString *urlString = [NSString stringWithFormat:
            @"x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=%@", bundleID];
        url = [NSURL URLWithString:urlString];
    } else if (@available(macOS 12.0, *)) {
        NSString *urlString = [NSString stringWithFormat:
            @"x-apple.systempreferences:com.apple.preference.notifications?id=%@", bundleID];
        url = [NSURL URLWithString:urlString];
    } else {
        url = [NSURL fileURLWithPath:@"/System/Library/PreferencePanes/Notifications.prefPane"];
    }
    [[NSWorkspace sharedWorkspace] openURL:url];
}
