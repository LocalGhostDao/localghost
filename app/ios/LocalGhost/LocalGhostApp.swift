import SwiftUI

@main
@MainActor
struct LocalGhostApp: App {
  @State private var theme = GhostTheme()

  init() {
    // before anything reads the Keychain: what an earlier install left there goes (InstallMark)
    InstallMark.clearLeftovers(
      defaults: .standard,
      credentials: KeychainCredentialStore(),
      keyring: KeychainDeviceKeyring(),
      sessions: KeychainSessionStore()
    )
  }

  var body: some Scene {
    WindowGroup {
      RootView()
        .environment(theme)
        .preferredColorScheme(.dark)
        .tint(theme.accent)
    }
  }
}
