import SwiftUI

struct SetupView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var presentedSheet: SetupSheet?

  let model: SetupModel
  let onPaired: () -> Void

  var body: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 0) {
          TerminalSectionLabel(text: "Connect to your box")

          Text("Scan the box’s QR to enrol this device.\nNothing of your life is stored here.")
            .font(theme.font(.body))
            .foregroundStyle(theme.textDim)
            .padding(.top, 14)

          GhostButton(title: "Scan QR from the box") {
            presentedSheet = .scanner
          }
          .accessibilityIdentifier("setup.scan")
          .padding(.top, 24)

          GhostTextField(
            label: "Box address",
            placeholder: "https://your-box.local:8443",
            text: Bindable(model).boxAddress,
            keyboardType: .URL,
            textContentType: .URL
          )
          .accessibilityIdentifier("setup.address")
          .padding(.top, 28)

          if !model.boxName.isEmpty {
            Text("box: \(model.boxName)")
              .font(theme.font(.caption))
              .foregroundStyle(theme.textDim)
              .padding(.top, 8)
          }

          GhostTextField(
            label: "Device name",
            placeholder: "e.g. My iPhone",
            text: Bindable(model).deviceName,
            textContentType: .name
          )
          .accessibilityIdentifier("setup.deviceName")
          .padding(.top, 24)

          GhostReadOnlyField(
            label: "Box fingerprint",
            value: model.fingerprint,
            placeholder: "SHA-256 (from the QR)"
          )
          .padding(.top, 24)

          if let errorMessage = model.errorMessage {
            GhostErrorBanner(message: errorMessage)
              .accessibilityIdentifier("setup.error")
              .padding(.top, 18)
          }

          GhostButton(title: "Enrol this device") {
            if model.pair() {
              onPaired()
            }
          }
          .disabled(!model.canPair)
          .accessibilityIdentifier("setup.pair")
          .padding(.top, 28)

          if model.scannedLink == nil {
            Text("scan the box’s QR first: its device certificate is carried in the code and is never typed. The address and fingerprint fill in from the scan too.")
              .font(theme.font(.caption))
              .foregroundStyle(theme.accentDim)
              .padding(.top, 10)
          }

          Text("The only cloud is you.")
            .font(theme.font(.caption))
            .foregroundStyle(theme.accentDim)
            .frame(maxWidth: .infinity)
            .padding(.top, 26)
            .padding(.bottom, 28)
        }
        .padding(.horizontal, 20)
        .padding(.top, 20)
      }
      .scrollDismissesKeyboard(.interactively)
    }
    .toolbar(.hidden, for: .navigationBar)
    .fullScreenCover(item: $presentedSheet) { sheet in
      switch sheet {
      case .scanner:
        QRScannerSheet { payload in
          model.acceptEnrolmentPayload(payload)
        }
        .environment(theme)
      }
    }
  }
}

private enum SetupSheet: String, Identifiable {
  case scanner

  var id: String { rawValue }
}

#Preview {
  NavigationStack {
    SetupView(
      model: SetupModel(credentialStore: PreviewCredentialStore()),
      onPaired: {}
    )
  }
  .environment(GhostTheme())
  .preferredColorScheme(.dark)
}

private struct PreviewCredentialStore: CredentialStoring {
  func load() throws -> EnrolmentCredentials? { nil }
  func save(_: EnrolmentCredentials) throws {}
  func clear() throws {}
}
