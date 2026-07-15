// The UIApplicationDelegate that bridges APNs into the app. It owns the one
// PushCenter the SwiftUI tree reads, hands APNs the device token as it arrives,
// renders a foreground alert as a banner, and turns a tapped notification into a
// deep link the connected machine screen routes on.

import UIKit
import UserNotifications

/// AppDelegate wires APNs: it registers as the notification-center delegate, forwards
/// the device token to the shared PushCenter, and surfaces foreground alerts and taps.
@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    let push = PushCenter()

    func application(
        _: UIApplication,
        didFinishLaunchingWithOptions _: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(
        _: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        push.setDeviceToken(deviceToken)
    }

    /// willPresent renders an alert that lands while the app is foregrounded as a
    /// banner with sound; the in-app feed is fed by the subject stream, so the push
    /// itself only needs to show. The delegate protocol is not main-actor-isolated, so
    /// this hop-free method is nonisolated.
    nonisolated func userNotificationCenter(
        _: UNUserNotificationCenter,
        willPresent _: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound, .badge]
    }

    /// didReceive turns a tapped notification into a deep link to the subject that
    /// fired it, hopping to the main actor to raise it on the shared PushCenter.
    nonisolated func userNotificationCenter(
        _: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let userInfo = response.notification.request.content.userInfo
        guard let link = PushCenter.deepLink(from: userInfo) else {
            return
        }
        await MainActor.run {
            push.openDeepLink(link)
        }
    }
}
