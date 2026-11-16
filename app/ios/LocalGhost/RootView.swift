import SwiftUI

enum AppPhase: Equatable {
  case setup
  case locked
  case credentialError

  static let initial = AppPhase.setup
}

struct RootView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var phase: AppPhase
  @State private var setupModel: SetupModel

  init(
    phase: AppPhase? = nil,
    credentialStore: any CredentialStoring = KeychainCredentialStore()
  ) {
    let storedPhase: AppPhase
    if let phase {
      storedPhase = phase
    } else {
      do {
        storedPhase = try credentialStore.load() == nil ? .setup : .locked
      } catch {
        // Fail closed: an unreadable Keychain record must not look like a fresh install and invite
        // the user to overwrite an existing device identity.
        storedPhase = .credentialError
      }
    }
    _phase = State(initialValue: storedPhase)
    _setupModel = State(initialValue: SetupModel(credentialStore: credentialStore))
  }

  var body: some View {
    NavigationStack {
      switch phase {
      case .setup:
        SetupView(model: setupModel) {
          phase = .locked
        }
      case .locked:
        TerminalStateView(
          heading: "DEVICE ENROLLED",
          message: "this iPhone is paired with your box. App locking and the secure box channel come next."
        )
      case .credentialError:
        TerminalStateView(
          heading: "CREDENTIALS UNAVAILABLE",
          message: "LocalGhost could not read the existing device credentials. No data was changed.",
          warning: true
        )
      }
    }
    .toolbar(.hidden, for: .navigationBar)
    .background(theme.background)
  }
}

private struct TerminalStateView: View {
  @Environment(GhostTheme.self) private var theme

  let heading: String
  let message: String
  var warning = false

  var body: some View {
    GhostScreen {
      VStack(spacing: 22) {
        Spacer()

        GhostMark()

        Text("LOCALGHOST")
          .font(theme.font(.largeTitle, weight: .bold))
          .foregroundStyle(theme.accent)

        Text("> the only cloud is you")
          .font(theme.font(.callout))
          .foregroundStyle(theme.textDim)

        Spacer().frame(height: 18)

        Text("> \(heading)")
          .font(theme.font(.body))
          .foregroundStyle(warning ? theme.warning : theme.accentDim)

        Text(message)
          .font(theme.font(.callout))
          .foregroundStyle(warning ? theme.warning : theme.textDim)
          .multilineTextAlignment(.center)
          .padding(.horizontal, 30)

        Spacer()
      }
      .padding(.vertical, 24)
    }
  }
}

#Preview {
  RootView(phase: .setup, credentialStore: PreviewRootCredentialStore())
    .environment(GhostTheme())
    .preferredColorScheme(.dark)
}

private struct PreviewRootCredentialStore: CredentialStoring {
  func load() throws -> EnrolmentCredentials? { nil }
  func save(_: EnrolmentCredentials) throws {}
  func clear() throws {}
}
