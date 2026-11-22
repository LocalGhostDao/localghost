import SwiftUI

/// Enrolled: the PIN, the box opening step by step, then what it runs.
struct BoxView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var confirmingForget = false
  @State private var forgetError: String?

  let model: BoxModel
  let onForgotten: () -> Void

  static let forgetWords = "This iPhone's device key, its certificate and the session are deleted from this iPhone. "
    + "The box still lists this iPhone until it is retired there; enrolling again takes a new QR from the box."

  var body: some View {
    Group {
      if model.screen == .open {
        AppShellView(
          boxModel: model,
          onLock: { model.lock() },
          onForgotten: onForgotten
        )
      } else {
        accessView
      }
    }
    .toolbar(.hidden, for: .navigationBar)
    .task {
      await model.start()
    }
  }

  private var accessView: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 0) {
          switch model.screen {
          case .unlock:
            unlockSection
          case .opening:
            openingSection
          case .open:
            EmptyView()
          }

          if let message = forgetError ?? model.errorMessage {
            GhostErrorBanner(message: message)
              .accessibilityIdentifier("box.error")
              .padding(.top, 18)
          }

          if let note = model.keyNote {
            Text("> \(note)")
              .font(theme.font(.caption))
              .foregroundStyle(theme.accentDim)
              .padding(.top, 18)
          }

          if model.screen != .opening {
            GhostButton(title: "Forget this box") {
              confirmingForget = true
            }
            .accessibilityIdentifier("box.forget")
            .padding(.top, 40)
          }
        }
        .padding(.horizontal, 20)
        .padding(.top, 20)
        .padding(.bottom, 28)
      }
      .scrollDismissesKeyboard(.interactively)
    }
    .confirmationDialog("Forget this box?", isPresented: $confirmingForget, titleVisibility: .visible) {
      Button("Forget this box", role: .destructive) {
        Task {
          do {
            try await model.forget()
            onForgotten()
          } catch {
            // the model says why, and what to do next (its errorMessage, shown below). A line
            // kept here stayed over every later one, a wrong PIN's included.
            forgetError = nil
          }
        }
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text(Self.forgetWords)
    }
  }

  private var unlockSection: some View {
    VStack(alignment: .leading, spacing: 0) {
      TerminalSectionLabel(text: "Open your box")

      Text("The PIN goes to the box and nowhere else; this iPhone does not keep it.")
        .font(theme.font(.body))
        .foregroundStyle(theme.textDim)
        .padding(.top, 14)

      VStack(alignment: .leading, spacing: 8) {
        Text("PIN")
          .font(theme.font(.caption))
          .foregroundStyle(theme.accentDim)
        SecureField("", text: Bindable(model).pin, prompt: Text("your PIN").foregroundStyle(theme.textDim))
          .font(theme.font(.title3))
          .foregroundStyle(theme.text)
          .tint(theme.accent)
          .keyboardType(.numberPad)
          .textContentType(.password)
          .padding(.horizontal, 16)
          .frame(minHeight: 58)
          .overlay {
            RoundedRectangle(cornerRadius: 12).stroke(theme.border, lineWidth: 1)
          }
          .accessibilityIdentifier("box.pin")
      }
      .padding(.top, 28)

      GhostButton(title: "Open the box") {
        Task { await model.unlock() }
      }
      .disabled(!model.canUnlock)
      .accessibilityIdentifier("box.unlock")
      .padding(.top, 24)
    }
  }

  private var openingSection: some View {
    VStack(alignment: .leading, spacing: 10) {
      TerminalSectionLabel(text: "The box is opening")

      if model.steps.isEmpty {
        Text("> asking the box...")
          .font(theme.font(.callout))
          .foregroundStyle(theme.textDim)
      }
      ForEach(model.steps) { step in
        HStack {
          Text("> \(step.words)")
            .foregroundStyle(step.state == "PENDING" ? theme.textDim : theme.text)
          Spacer()
          Text(step.mark)
            .foregroundStyle(step.state == "ERRORED" ? theme.warning : theme.accent)
        }
        .font(theme.font(.callout))
      }
    }
  }

}

struct BoxStatusView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var confirmingForget = false
  @State private var forgetError: String?

  let model: BoxModel
  let onForgotten: () -> Void

  var body: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 10) {
          TerminalSectionLabel(text: "Box status")

          if let status = model.status {
            ForEach(status.services, id: \.name) { service in
              VStack(alignment: .leading, spacing: 2) {
                HStack {
                  Text(service.name)
                    .foregroundStyle(theme.text)
                  Spacer()
                  Text(service.state.lowercased())
                    .foregroundStyle(Self.isWell(service.state) ? theme.accent : theme.warning)
                }
                if let detail = service.detail ?? service.lastErr, !detail.isEmpty {
                  Text(detail)
                    .font(theme.font(.caption))
                    .foregroundStyle(theme.textDim)
                }
              }
              .font(theme.font(.callout))
            }
            ForEach(status.datastores ?? [], id: \.name) { store in
              HStack {
                Text(store.name)
                  .foregroundStyle(theme.text)
                Spacer()
                Text(store.state.lowercased())
                  .foregroundStyle(Self.isWell(store.state) ? theme.accent : theme.warning)
              }
              .font(theme.font(.callout))
            }
          } else {
            Text("> asking the box...")
              .font(theme.font(.callout))
              .foregroundStyle(theme.textDim)
          }

          if let message = forgetError ?? model.errorMessage {
            GhostErrorBanner(message: message)
              .accessibilityIdentifier("box.error")
              .padding(.top, 8)
          }

          if let note = model.keyNote {
            Text("> \(note)")
              .font(theme.font(.caption))
              .foregroundStyle(theme.accentDim)
              .padding(.top, 8)
          }

          GhostButton(title: "Refresh") {
            Task { await model.refresh() }
          }
          .accessibilityIdentifier("box.refresh")
          .padding(.top, 18)

          GhostButton(title: "Forget this box") {
            confirmingForget = true
          }
          .accessibilityIdentifier("box.forget")
          .padding(.top, 20)
        }
        .padding(20)
        .padding(.bottom, 20)
      }
    }
    .confirmationDialog("Forget this box?", isPresented: $confirmingForget, titleVisibility: .visible) {
      Button("Forget this box", role: .destructive) {
        Task {
          do {
            try await model.forget()
            onForgotten()
          } catch {
            // the model says why, and what to do next (its errorMessage, shown below). A line
            // kept here stayed over every later one, a wrong PIN's included.
            forgetError = nil
          }
        }
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text(BoxView.forgetWords)
    }
  }

  private static func isWell(_ state: String) -> Bool {
    ["running", "up", "ok", "ready"].contains(state.lowercased())
  }
}
