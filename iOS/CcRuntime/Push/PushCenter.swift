// The app-level push state the AppDelegate feeds and the screens read: the APNs
// device token (hex, as the daemon wants it), the notification authorization, and a
// pending deep link a tapped notification sets. The connected machine screen watches
// the token to register it and the deep link to route to the question that fired.

import CcRuntimeKit
import Foundation
import UIKit
import UserNotifications

/// PushCenter holds the device's push registration and the routing signal a tapped
/// notification raises. It is one instance shared between the UIApplicationDelegate
/// (which writes the token and deep link) and the SwiftUI tree (which reads them).
@MainActor
@Observable
final class PushCenter {
    /// DeepLink is the subject a tapped notification wants opened.
    struct DeepLink: Equatable {
        let subject: String
    }

    private(set) var deviceTokenHex: String?
    private(set) var authorized = false
    private(set) var pendingDeepLink: DeepLink?

    /// requestAuthorization asks for notification permission and, when granted,
    /// registers for remote notifications so APNs delivers a device token. It is safe
    /// to call on every connect; the token then flows back through the AppDelegate.
    func requestAuthorization() async {
        let center = UNUserNotificationCenter.current()
        let granted = await (try? center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        authorized = granted
        if granted {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    /// setDeviceToken records the raw APNs token as lowercase hex, the form the
    /// registrar posts.
    func setDeviceToken(_ token: Data) {
        deviceTokenHex = DeviceTokenRegistrar.hex(token)
    }

    /// openDeepLink raises the subject a tapped notification names, for the presented
    /// machine screen to route to.
    func openDeepLink(_ link: DeepLink) {
        pendingDeepLink = link
    }

    /// clearDeepLink drops the pending route once a screen has consumed it.
    func clearDeepLink() {
        pendingDeepLink = nil
    }

    /// deepLink reads the target subject out of an APNs payload: the structured
    /// `payload.subject` the daemon sends, falling back to the `aps.thread-id` the
    /// alert threads on.
    nonisolated static func deepLink(from userInfo: [AnyHashable: Any]) -> DeepLink? {
        if let subject = nonEmptyString(userInfo["payload"], key: "subject") {
            return DeepLink(subject: subject)
        }
        if let thread = nonEmptyString(userInfo["aps"], key: "thread-id") {
            return DeepLink(subject: thread)
        }
        return nil
    }

    private nonisolated static func nonEmptyString(_ any: Any?, key: String) -> String? {
        guard let dict = any as? [String: Any], let value = dict[key] as? String, !value.isEmpty else {
            return nil
        }
        return value
    }
}
