import SwiftUI

struct PermissionsView: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.scenePhase) private var scenePhase
  let model: PermissionsModel

  var body: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 18) {
          TerminalSectionLabel(text: "Permissions")
          Text("What this iPhone allows, why it is needed, and where to change it. You choose each permission separately.")
            .font(theme.font(.callout))
            .foregroundStyle(theme.textDim)

          ForEach(AppPermission.allCases) { permission in
            permissionRow(permission)
          }

          if let error = model.errorMessage { GhostErrorBanner(message: error) }

          TerminalSectionLabel(text: "Not used on iOS yet")
            .padding(.top, 6)
          Text("Photos and files, microphone, contacts, calendar, Health, and notifications are not requested by this build. Their sync and capture features are not available yet.")
            .font(theme.font(.callout))
            .foregroundStyle(theme.textDim)
          Text("The app does not request background location or track you across other apps. Background sync is not available yet.")
            .font(theme.font(.caption))
            .foregroundStyle(theme.textDim)
          GhostButton(title: "Open app settings", wrapsTitle: true) {
            Task { await model.openSettings() }
          }
          .accessibilityIdentifier("permissions.settings")
        }
        .padding(20)
        .frame(maxWidth: 700, alignment: .leading)
        .frame(maxWidth: .infinity)
      }
    }
    .task { model.refresh() }
    .onChange(of: scenePhase) { _, phase in
      if phase == .active { model.refresh() }
    }
  }

  private func permissionRow(_ permission: AppPermission) -> some View {
    let access = model.access(for: permission)
    return VStack(alignment: .leading, spacing: 10) {
      Text(permission.title)
        .font(theme.font(.body, weight: .semibold))
        .accessibilityAddTraits(.isHeader)
      Text(access.label.uppercased())
        .font(theme.font(.caption))
        .foregroundStyle(access == .denied || access == .restricted ? theme.warning : theme.accent)
        .accessibilityIdentifier("permissions.\(permission.rawValue).status")
      Text(permission.reason)
        .font(theme.font(.callout))
        .foregroundStyle(theme.textDim)
      if permission == .localNetwork {
        Text("iOS does not expose the current grant to this app. Check the Local Network switch in system settings; it appears after iOS has asked.")
          .font(theme.font(.caption))
          .foregroundStyle(theme.textDim)
      }
      if permission == .location, access == .denied {
        Text("If location is off for the whole iPhone, enable Location Services in Settings › Privacy & Security first.")
          .font(theme.font(.caption))
          .foregroundStyle(theme.textDim)
      }
      if access == .restricted {
        Text("A device policy or parental control restricts access. It cannot be requested here.")
          .font(theme.font(.caption))
          .foregroundStyle(theme.warning)
      }
      if let action = access.action {
        GhostButton(title: model.asking == permission ? "Opening…" : action, wrapsTitle: true) {
          Task { await model.perform(permission) }
        }
        .disabled(model.asking != nil)
        .accessibilityIdentifier("permissions.\(permission.rawValue).action")
      }
    }
    .padding(16)
    .frame(maxWidth: .infinity, alignment: .leading)
    .background(theme.surface)
    .overlay { Rectangle().stroke(theme.border, lineWidth: 1) }
  }
}

#Preview("Permissions") {
  PermissionsView(model: PermissionsModel(client: PermissionClient(
    status: { permission in permission == .localNetwork ? .systemSettings : .denied },
    request: { _ in }, openSettings: { true }
  )))
  .environment(GhostTheme())
  .preferredColorScheme(.dark)
}
