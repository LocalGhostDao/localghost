import Foundation
import Testing
@testable import LocalGhost

@MainActor
struct SettingsTests {
  private let now = Date(timeIntervalSince1970: 1_794_800_000)

  @Test
  func preferencesHaveWorkingDefaultsAndPersistIndependently() throws {
    let suite = "localghost-settings-tests-\(UUID().uuidString)"
    let defaults = try #require(UserDefaults(suiteName: suite))
    defer { defaults.removePersistentDomain(forName: suite) }
    let preferences = AppPreferences(defaults: defaults)
    #expect(preferences.weatherLocation)
    #expect(preferences.fetchPublicFeeds)
    #expect(!preferences.reduceMotion)
    preferences.weatherLocation = false
    preferences.fetchPublicFeeds = false
    preferences.reduceMotion = true
    let restored = AppPreferences(defaults: defaults)
    #expect(!restored.weatherLocation)
    #expect(!restored.fetchPublicFeeds)
    #expect(restored.reduceMotion)
  }

  @Test
  func lockFailureInASuccessfulHTTPResponsePreservesTheSession() async throws {
    let (model, sessions, _) = makeModel { BoxLockAnswer(locked: false, error: "unmount failed") }
    await model.start()
    await #expect(throws: SettingsError.self) { try await model.lockBox() }
    #expect(model.screen == .open)
    #expect(sessions.load()?.token == "old-session")
  }

  @Test
  func onlyAConfirmedBoxLockDropsTheLocalSession() async throws {
    let (model, sessions, _) = makeModel { BoxLockAnswer(locked: true, error: nil) }
    await model.start()
    #expect(try await model.lockBox())
    #expect(model.screen == .unlock)
    #expect(sessions.load() == nil)
  }

  @Test
  func aNetworkFailureDoesNotClaimThatTheBoxLocked() async throws {
    let (model, sessions, _) = makeModel { throw BoxChannelError.unreachable("timeout") }
    await model.start()
    await #expect(throws: BoxChannelError.self) { try await model.lockBox() }
    #expect(model.screen == .open)
    #expect(sessions.load() != nil)
  }

  @Test
  func aDelayedBoxLockCannotDropANewerSession() async throws {
    let gate = SettingsLockGate()
    let (model, sessions, box) = makeModel { await gate.answer() }
    await model.start()
    let oldLock = Task { try await model.lockBox() }
    await gate.waitUntilRequested()
    model.lock()
    model.pin = "123456"
    box.polls = [UnlockPoll(stages: [], done: true, failed: nil,
      token: "new-session", expiresAt: "2099-01-01T00:00:00Z")]
    await model.unlock()
    await gate.release()
    #expect(try await oldLock.value == false)
    #expect(model.screen == .open)
    #expect(sessions.load()?.token == "new-session")
  }

  @Test
  func settingsReportsTheEffectiveSessionDeadlineAndKeyHandOver() throws {
    var credentials = EnrolmentFixture.credentials(tag: "device")
    credentials.rekeyedAt = now
    credentials.pendingKeyTag = "pending"
    let deadline = now.addingTimeInterval(3600)
    let snapshot = BoxSettingsSnapshot(credentials: credentials,
      session: BoxSession(token: "never-display-this", expiresAt: deadline))
    #expect(snapshot.address == credentials.baseURL)
    #expect(snapshot.fingerprint == credentials.certificateFingerprint)
    #expect(snapshot.sessionEnds == deadline.addingTimeInterval(-60))
    #expect(snapshot.pendingKey)
    #expect(snapshot.keyMade == now)
  }

  @Test
  func lockAnswerDecodesBothServerOutcomes() throws {
    let decoder = JSONDecoder()
    let failed = try decoder.decode(BoxLockAnswer.self,
      from: Data(#"{"locked":false,"steps":[],"error":"unmount failed"}"#.utf8))
    #expect(!failed.locked)
    #expect(failed.error == "unmount failed")
    let locked = try decoder.decode(BoxLockAnswer.self,
      from: Data(#"{"locked":true,"steps":[]}"#.utf8))
    #expect(locked.locked)
    #expect(locked.error == nil)
  }

  private func makeModel(
    lockReply: @escaping @MainActor @Sendable () async throws -> BoxLockAnswer
  ) -> (BoxModel, MemorySessionStore, FakeBox) {
    var credentials = EnrolmentFixture.credentials(tag: "device")
    credentials.rekeyedAt = now
    let sessions = MemorySessionStore(BoxSession(token: "old-session", expiresAt: .distantFuture))
    let box = FakeBox()
    let now = self.now
    let model = BoxModel(
      credentialStore: MemoryCredentialStore(credentials), keyring: RecordingKeyring(), sessions: sessions,
      connect: { _, tag, token in box.over(tag: tag, token: token) },
      signedInConnect: { _, _, _, _ in SettingsTestChannel(reply: lockReply) },
      now: { now }, pollEvery: .zero
    )
    return (model, sessions, box)
  }
}

private actor SettingsLockGate {
  private var requested = false
  private var observer: CheckedContinuation<Void, Never>?
  private var response: CheckedContinuation<BoxLockAnswer, Never>?
  func answer() async -> BoxLockAnswer {
    requested = true
    observer?.resume()
    observer = nil
    return await withCheckedContinuation { response = $0 }
  }
  func waitUntilRequested() async {
    guard !requested else { return }
    await withCheckedContinuation { observer = $0 }
  }
  func release() {
    response?.resume(returning: BoxLockAnswer(locked: true, error: nil))
    response = nil
  }
}

private nonisolated struct SettingsTestChannel: SignedInCalls {
  let reply: @MainActor @Sendable () async throws -> BoxLockAnswer
  func lockBox() async throws -> BoxLockAnswer { try await reply() }
  func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { $0.finish() }
  }
  func chats(query _: String, beforeUpdated _: Int64, limit _: Int) async throws -> [ChatSummary] { [] }
  func chatMessages(chatID _: Int64, beforeID _: Int64, limit _: Int) async throws -> [SavedChatMessage] { [] }
  func stopChat(chatID _: Int64) async throws -> Bool { true }
  func renameChat(chatID _: Int64, title _: String) async throws {}
  func deleteChat(chatID _: Int64) async throws {}
  func fetchHome() async throws -> HomeSnapshot { throw BoxChannelError.appearsDown }
  func fetchRates() async throws -> HomeRates { throw BoxChannelError.appearsDown }
  func fetchFastRates() async throws -> HomeFastRates { throw BoxChannelError.appearsDown }
  func fetchNews(since _: Int64) async throws -> HomeNews { throw BoxChannelError.appearsDown }
  func fetchWeather(at _: HomeLocation) async throws -> HomeWeather { throw BoxChannelError.appearsDown }
  func writeNewsBrief() async throws -> HomeBriefResult { throw BoxChannelError.appearsDown }
}

// These attachments are reviewed with the simulator test results, including large Dynamic Type.
import SwiftUI
import UIKit
import XCTest

nonisolated final class SettingsLayoutTests: XCTestCase {
  @MainActor
  func testSettingsAndPermissionsLayouts() async throws {
    let suite = "localghost-layout-tests-\(UUID().uuidString)"
    let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
    defer { defaults.removePersistentDomain(forName: suite) }
    let preferences = AppPreferences(defaults: defaults)
    let theme = GhostTheme(preferences: preferences)
    var credentials = EnrolmentFixture.credentials(tag: "layout-test")
    credentials.rekeyedAt = Date()
    let box = BoxModel(
      credentialStore: MemoryCredentialStore(credentials), keyring: RecordingKeyring(),
      sessions: MemorySessionStore(BoxSession(token: "fixture", expiresAt: Date().addingTimeInterval(86_400)))
    )
    let chat = ChatModel(client: { throw BoxChannelError.notEnrolled }, preferences: defaults)
    let permissions = PermissionsModel(client: PermissionClient(
      status: { permission in
        switch permission {
        case .camera: .denied
        case .location: .approximate
        case .localNetwork: .systemSettings
        }
      }, request: { _ in XCTFail("Rendering must not request permission") },
      openSettings: { XCTFail("Rendering must not open Settings"); return false }
    ))
    for size in [DynamicTypeSize.large, .accessibility3] {
      try await capture(
        SettingsView(box: box, chat: chat, preferences: preferences,
          onPermissions: {}, onLock: {}, onForgotten: {}),
        name: "settings-\(size)", theme: theme, size: size)
      try await capture(PermissionsView(model: permissions),
        name: "permissions-\(size)", theme: theme, size: size)
    }
  }

  @MainActor
  private func capture<Content: View>(
    _ content: Content, name: String, theme: GhostTheme, size: DynamicTypeSize
  ) async throws {
    let controller = UIHostingController(rootView: content
      .environment(theme).dynamicTypeSize(size).preferredColorScheme(.dark))
    let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
    let previousWindow = scene.windows.first(where: \.isKeyWindow)
    let window = UIWindow(windowScene: scene)
    window.frame = CGRect(x: 0, y: 0, width: 393, height: 852)
    window.rootViewController = controller
    window.makeKeyAndVisible()
    defer { window.isHidden = true; previousWindow?.makeKeyAndVisible() }
    controller.view.setNeedsLayout()
    controller.view.layoutIfNeeded()
    // Allow SwiftUI's on-appear task and layout transaction to finish before capturing.
    try await Task.sleep(for: .milliseconds(150))
    controller.view.layoutIfNeeded()
    let scroll = try XCTUnwrap(scrollView(in: controller.view))
    XCTAssertGreaterThan(scroll.contentSize.height, 0)
    for (position, fraction) in [("top", 0.0), ("middle", 0.5), ("bottom", 1.0)] {
      let bottom = max(0, scroll.contentSize.height - scroll.bounds.height + scroll.adjustedContentInset.bottom)
      scroll.setContentOffset(CGPoint(x: 0, y: bottom * fraction), animated: false)
      controller.view.layoutIfNeeded()
      let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
        XCTAssertTrue(window.drawHierarchy(in: window.bounds, afterScreenUpdates: true))
      }
      let attachment = XCTAttachment(image: image)
      attachment.name = "\(name)-\(position)"
      attachment.lifetime = .keepAlways
      add(attachment)
    }
  }

  @MainActor
  private func scrollView(in view: UIView) -> UIScrollView? {
    if let scroll = view as? UIScrollView { return scroll }
    return view.subviews.lazy.compactMap { self.scrollView(in: $0) }.first
  }
}
