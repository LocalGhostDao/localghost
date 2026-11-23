import SwiftUI

struct SettingsView: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.scenePhase) private var scenePhase
  @State private var snapshot: BoxSettingsSnapshot?
  @State private var errorMessage: String?
  @State private var confirmation: Confirmation?
  @State private var working = false

  let box: BoxModel
  let chat: ChatModel
  let preferences: AppPreferences
  let onPermissions: () -> Void
  let onLock: () -> Void
  let onForgotten: () -> Void

  private enum Confirmation: String, Identifiable {
    case lockBox
    case forget
    var id: Self { self }
    var title: String { self == .lockBox ? "Lock the box?" : "Forget this box?" }
    var message: String {
      switch self {
      case .lockBox:
        "Stops the box's databases, unmounts its drive and drops the vault key from memory. Other connected devices lose access too. Your data stays on the box. Enter the PIN to open it again."
      case .forget:
        "Deletes this iPhone's enrolment, device key, certificate, session, Home cache and last-chat reference. Your box and its data stay untouched. Enrolling again needs a new QR from the box."
      }
    }
  }

  var body: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 20) {
          TerminalSectionLabel(text: "Settings")
          Text("Your box, what this iPhone sends, and how the app behaves.")
            .font(theme.font(.callout))
            .foregroundStyle(theme.textDim)

          if let errorMessage { GhostErrorBanner(message: errorMessage) }

          settingsSection("Your box") {
            if let snapshot {
              detail("Address", snapshot.address)
              detail("This device", snapshot.deviceName)
              if let date = snapshot.sessionEnds {
                detail("Session ends", date.formatted(date: .abbreviated, time: .shortened))
              } else {
                detail("Session", "No saved session")
              }
              detail("Device key", snapshot.keyMade.map {
                "Made on this iPhone · \($0.formatted(date: .abbreviated, time: .shortened))"
              } ?? "Imported from the enrolment QR")
              if let date = snapshot.certificateEnds {
                detail("Certificate expires", date.formatted(date: .abbreviated, time: .shortened))
              }
              if snapshot.pendingKey {
                explanation("A device-key hand-over is pending. The app retries confirmation at the next unlock.")
              }
              DisclosureGroup("Box certificate fingerprint") {
                Text(snapshot.fingerprint)
                  .font(theme.font(.caption))
                  .textSelection(.enabled)
                  .padding(.top, 8)
                  .accessibilityIdentifier("settings.fingerprint")
              }
              .font(theme.font(.callout))
            } else {
              explanation("Enrolment details could not be read.")
            }
            explanation("The connection is pinned to your box and uses this device's certificate. The session token and private key are never shown here.")
            GhostButton(title: "Lock this app", wrapsTitle: true, action: onLock)
              .accessibilityIdentifier("settings.lockApp")
            explanation("Returns this iPhone to the PIN. The box keeps running.")
            GhostButton(title: "Lock the box now", wrapsTitle: true) { confirmation = .lockBox }
              .accessibilityIdentifier("settings.lockBox")
          }

          settingsSection("This iPhone") {
            Toggle(isOn: Bindable(preferences).weatherLocation) {
              Text("Use location for weather")
                .font(theme.font(.callout))
            }
            .accessibilityIdentifier("settings.weatherLocation")
            explanation("Uses a coarse fix while the app is open and sends it only to your box. Turning this off stops new fixes; the last forecast may remain cached.")
            GhostButton(title: "Permissions", wrapsTitle: true, action: onPermissions)
              .accessibilityIdentifier("settings.permissions")
          }

          settingsSection("News and rates") {
            Toggle(isOn: Bindable(preferences).fetchPublicFeeds) {
              Text("Fetch public feeds for the box")
                .font(theme.font(.callout))
            }
            .accessibilityIdentifier("settings.fetchFeeds")
            explanation("On a Home pull to refresh, this iPhone fetches the box's public sources if its feed mark is at least half an hour old. Publishers and exchanges see this phone's network address. Turning this off still lets Home read data already on the box.")
          }

          settingsSection("Chat") {
            Text("Answer length").font(theme.font(.callout))
            Picker("Answer length", selection: Binding(
              get: { chat.answerLength }, set: { chat.setAnswerLength($0) }
            )) {
              ForEach(ChatAnswerLength.allCases, id: \.self) { length in
                Text(length.rawValue.capitalized).tag(length)
              }
            }
            .labelsHidden()
            .font(theme.font(.callout))
            .tint(theme.accent)
            .accessibilityLabel("Answer length")
            .accessibilityIdentifier("settings.answerLength")
            Text("Thinking").font(theme.font(.callout))
            Picker("Thinking", selection: Binding(
              get: { chat.thinking }, set: { chat.setThinking($0) }
            )) {
              ForEach(ChatThinking.allCases, id: \.self) { level in
                Text(level.label).tag(level)
              }
            }
            .labelsHidden()
            .font(theme.font(.callout))
            .tint(theme.accent)
            .accessibilityLabel("Thinking")
            .accessibilityIdentifier("settings.thinking")
            explanation("Applies to your next question, including questions from Home. Deeper thinking asks the box for more reasoning and can take longer. Chat uses the model on your box; web search, voice and attachments are not available yet.")
          }

          settingsSection("Screen") {
            Toggle(isOn: Bindable(preferences).reduceMotion) {
              Text("Reduce motion")
                .font(theme.font(.callout))
            }
            .accessibilityIdentifier("settings.reduceMotion")
            explanation("Reduces app animations. The iPhone's Reduce Motion setting is always respected. Text size follows your system settings.")
          }

          settingsSection("Your data") {
            explanation("Photo and file sync, background transfers, the location trail, Health, contacts and calendar sync are not available on iOS yet. This build does not upload them.")
            explanation("There is no app export yet. Originals and encrypted backups are managed at the box. PIN changes and resetting or erasing the box happen at the box, never from this phone.")
            explanation("This iPhone keeps its enrolment and session in the Keychain, plus a cache of Home's news, prices and last forecast. Chat transcripts stay on the box. Phone preferences stay on this phone.")
          }

          settingsSection("Forget this device") {
            explanation("Remove this iPhone's access and cached Home data. The box keeps its data and device record until you retire this iPhone there.")
            Button(role: .destructive) { confirmation = .forget } label: {
              Text("[ FORGET THIS BOX ]")
                .font(theme.font(.body))
                .frame(maxWidth: .infinity, minHeight: 54)
                .foregroundStyle(theme.warning)
                .overlay { Rectangle().stroke(theme.warning, lineWidth: 1) }
            }
            .accessibilityIdentifier("settings.forget")
          }
        }
        .disabled(working)
        .padding(20)
        .frame(maxWidth: 700, alignment: .leading)
        .frame(maxWidth: .infinity)
      }
    }
    .overlay {
      if working {
        ProgressView("Working…")
          .padding(20)
          .background(theme.surface)
          .accessibilityIdentifier("settings.working")
      }
    }
    .confirmationDialog(confirmation?.title ?? "", isPresented: Binding(
      get: { confirmation != nil }, set: { if !$0 { confirmation = nil } }
    ), titleVisibility: .visible, presenting: confirmation) { action in
      Button(action == .lockBox ? "Lock the box" : "Forget this box", role: .destructive) {
        Task { await perform(action) }
      }
      Button("Cancel", role: .cancel) {}
    } message: { action in
      Text(action.message)
    }
    .task { loadSnapshot() }
    .onChange(of: scenePhase) { _, phase in
      if phase == .active { loadSnapshot() }
    }
  }

  private func settingsSection<Content: View>(
    _ title: String, @ViewBuilder content: () -> Content
  ) -> some View {
    VStack(alignment: .leading, spacing: 14) {
      TerminalSectionLabel(text: title)
      content()
    }
    .padding(16)
    .background(theme.surface)
    .overlay { Rectangle().stroke(theme.border, lineWidth: 1) }
  }

  private func detail(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      Text(title.uppercased()).font(theme.font(.caption)).foregroundStyle(theme.textDim)
      Text(value).font(theme.font(.callout)).textSelection(.enabled)
    }
    .frame(maxWidth: .infinity, alignment: .leading)
  }

  private func explanation(_ text: String) -> some View {
    Text(text).font(theme.font(.caption)).foregroundStyle(theme.textDim)
      .fixedSize(horizontal: false, vertical: true)
  }

  private func loadSnapshot() {
    do { snapshot = try box.settingsSnapshot() }
    catch { errorMessage = error.localizedDescription }
  }

  private func perform(_ action: Confirmation) async {
    guard !working else { return }
    working = true
    errorMessage = nil
    defer { working = false }
    do {
      switch action {
      case .lockBox:
        if try await box.lockBox() { chat.resetForLock() }
      case .forget:
        try await box.forget()
        chat.resetForLock()
        onForgotten()
      }
    } catch is CancellationError {
      return
    } catch {
      errorMessage = error.localizedDescription
    }
  }
}
