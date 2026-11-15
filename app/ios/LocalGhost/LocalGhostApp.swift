import SwiftUI

@main
@MainActor
struct LocalGhostApp: App {
  @State private var theme = GhostTheme()

  var body: some Scene {
    WindowGroup {
      RootView()
        .environment(theme)
        .preferredColorScheme(.dark)
        .tint(theme.accent)
    }
  }
}
