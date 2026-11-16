import Foundation
import SwiftUI

enum AppPhase: Equatable {
  case setup
  /// Enrolled: the box is opened with the PIN from here (BoxView).
  case locked
  case credentialError(String)

  static let initial = AppPhase.setup
}

struct RootView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var phase: AppPhase
  @State private var setupModel: SetupModel
  @State private var boxModel: BoxModel
  @State private var confirmingForget = false
  @State private var forgetError: String?

  private let credentialStore: any CredentialStoring

  init(
    phase: AppPhase? = nil,
    credentialStore: any CredentialStoring = KeychainCredentialStore(),
    keyring: any DeviceKeyring = KeychainDeviceKeyring(),
    sessions: any SessionStoring = KeychainSessionStore()
  ) {
    self.credentialStore = credentialStore
    _phase = State(initialValue: phase ?? Self.storedPhase(credentialStore))
    _setupModel = State(initialValue: SetupModel(credentialStore: credentialStore, keyring: keyring))
    _boxModel = State(initialValue: BoxModel(credentialStore: credentialStore, keyring: keyring, sessions: sessions))
  }

  var body: some View {
    NavigationStack {
      switch phase {
      case .setup:
        SetupView(model: setupModel) {
          phase = .locked
        }
      case .locked:
        BoxView(model: boxModel) {
          phase = .setup
        }
      case .credentialError(let detail):
        TerminalStateView(
          heading: "CREDENTIALS UNAVAILABLE",
          message: forgetError
            ?? detail,
          warning: true
        ) {
          GhostButton(title: "Try again") {
            phase = Self.storedPhase(credentialStore)
          }
          GhostButton(title: "Forget this box") {
            confirmingForget = true
          }
        }
        .confirmationDialog("Forget this box?", isPresented: $confirmingForget, titleVisibility: .visible) {
          Button("Forget this box", role: .destructive) {
            do {
              try boxModel.forget()
              phase = .setup
            } catch {
              forgetError = error.localizedDescription
            }
          }
          Button("Cancel", role: .cancel) {}
        } message: {
          Text(BoxView.forgetWords)
        }
      }
    }
    .toolbar(.hidden, for: .navigationBar)
    .background(theme.background)
  }

  private static func storedPhase(_ credentialStore: any CredentialStoring) -> AppPhase {
    do {
      return try credentialStore.load() == nil ? .setup : .locked
    } catch {
      // Fail closed: an unreadable Keychain record must not look like a fresh install and invite
      // the user to overwrite an existing device identity.
      let detail = String(reflecting: error)
      #if DEBUG
        NSLog("[LocalGhost Credentials] load failed: %@", detail)
        return .credentialError(detail)
      #else
        return .credentialError(
          "LocalGhost could not read this iPhone's device credentials. Nothing was changed."
        )
      #endif
    }
  }
}

struct TerminalStateView<Actions: View>: View {
  @Environment(GhostTheme.self) private var theme

  let heading: String
  let message: String
  var warning = false
  @ViewBuilder var actions: Actions

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

        VStack(spacing: 12) {
          actions
        }
        .padding(.horizontal, 24)

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
