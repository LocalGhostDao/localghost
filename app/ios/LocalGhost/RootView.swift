import SwiftUI

enum AppPhase: Equatable {
  case setup

  static let initial = AppPhase.setup
}

struct RootView: View {
  let phase: AppPhase

  init(phase: AppPhase = .initial) {
    self.phase = phase
  }

  var body: some View {
    NavigationStack {
      switch phase {
      case .setup:
        ContentUnavailableView {
          Label("LocalGhost", systemImage: "lock.shield")
        } description: {
          Text("Connect this app to your LocalGhost box.")
        }
        .navigationTitle("Setup")
      }
    }
  }
}

#Preview {
  RootView()
}
