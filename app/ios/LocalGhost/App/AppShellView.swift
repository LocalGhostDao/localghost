import SwiftUI

enum AppDestination: String, CaseIterable, Identifiable {
  case home
  case chat
  case gallery
  case map
  case phrases
  case health
  case mail
  case checkIn
  case memories
  case sync
  case integrations
  case boxStatus
  case models
  case notifications
  case connectors
  case codes
  case settings
  case permissions
  case glossary
  case about

  var id: Self { self }

  var label: String {
    switch self {
    case .home: "HOME"
    case .chat: "CHAT"
    case .gallery: "GALLERY"
    case .map: "MAP"
    case .phrases: "PHRASES"
    case .health: "HEALTH"
    case .mail: "MAIL"
    case .checkIn: "CHECK-IN"
    case .memories: "MEMORIES"
    case .sync: "SYNC"
    case .integrations: "INTEGRATIONS"
    case .boxStatus: "BOX STATUS"
    case .models: "MODELS"
    case .notifications: "NOTIFICATIONS"
    case .connectors: "CONNECTORS"
    case .codes: "CODES"
    case .settings: "SETTINGS"
    case .permissions: "PERMISSIONS"
    case .glossary: "GLOSSARY"
    case .about: "ABOUT"
    }
  }

  var glyph: String {
    switch self {
    case .home: "⌂"
    case .chat: "›_"
    case .gallery: "▦"
    case .map: "◎"
    case .phrases: "»"
    case .health: "♥"
    case .mail: "✉"
    case .checkIn: "◐"
    case .memories: "◇"
    case .sync: "⇅"
    case .integrations: "⊛"
    case .boxStatus: "◉"
    case .models: "▢"
    case .notifications: "△"
    case .connectors: "⊹"
    case .codes: "⚿"
    case .settings: "⚙"
    case .permissions: "⊡"
    case .glossary: "≣"
    case .about: "?"
    }
  }

  static let primary: [Self] = [.home, .chat]
  static let archive: [Self] = [.gallery, .map, .phrases, .health, .mail, .checkIn, .memories, .sync]
  static let box: [Self] = [.integrations, .boxStatus, .models, .notifications, .connectors, .codes]
  static let application: [Self] = [.settings, .permissions, .glossary, .about]
}

/// The signed-in application shell. Its information architecture mirrors Android; feature views
/// replace the placeholders one at a time without changing how people move around the app.
struct AppShellView: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.accessibilityReduceMotion) private var reduceMotion
  @State private var destination: AppDestination = .home
  @State private var drawerOpen = false

  let boxModel: BoxModel
  let onLock: () -> Void
  let onForgotten: () -> Void

  var body: some View {
    GeometryReader { proxy in
      let drawerWidth = min(360, proxy.size.width * 0.82)

      ZStack(alignment: .leading) {
        VStack(spacing: 0) {
          AppTopBar(title: destination.label) {
            setDrawer(open: true)
          }

          destinationView
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(theme.background)
        .allowsHitTesting(!drawerOpen)

        if drawerOpen {
          theme.background.opacity(0.72)
            .ignoresSafeArea()
            .contentShape(Rectangle())
            .onTapGesture { setDrawer(open: false) }
            .accessibilityLabel("Close menu")
            .accessibilityAddTraits(.isButton)
        }

        AppDrawer(
          selection: destination,
          connected: boxModel.status != nil,
          onSelect: select,
          onLock: {
            setDrawer(open: false)
            onLock()
          }
        )
        .frame(width: drawerWidth)
        .offset(x: drawerOpen ? 0 : -drawerWidth)
        .allowsHitTesting(drawerOpen)
        .accessibilityHidden(!drawerOpen)
        .gesture(
          DragGesture(minimumDistance: 12)
            .onEnded { value in
              if value.translation.width < -48 {
                setDrawer(open: false)
              }
            }
        )

        if !drawerOpen {
          Color.clear
            .contentShape(Rectangle())
            .frame(width: 22)
            .frame(maxHeight: .infinity)
            .gesture(
              DragGesture(minimumDistance: 12)
                .onEnded { value in
                  if value.translation.width > 48 {
                    setDrawer(open: true)
                  }
                }
            )
            .accessibilityHidden(true)
        }
      }
      .clipped()
    }
    .background(theme.background.ignoresSafeArea())
  }

  @ViewBuilder
  private var destinationView: some View {
    switch destination {
    case .boxStatus:
      BoxStatusView(model: boxModel, onForgotten: onForgotten)
    default:
      PendingDestinationView(destination: destination)
    }
  }

  private func select(_ newDestination: AppDestination) {
    destination = newDestination
    setDrawer(open: false)
  }

  private func setDrawer(open: Bool) {
    withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) {
      drawerOpen = open
    }
  }
}

private struct AppTopBar: View {
  @Environment(GhostTheme.self) private var theme

  let title: String
  let openMenu: () -> Void

  var body: some View {
    HStack(spacing: 14) {
      Button(action: openMenu) {
        Text("☰")
          .font(theme.font(.title2))
          .foregroundStyle(theme.accent)
          .frame(width: 44, height: 44)
          .contentShape(Rectangle())
      }
      .accessibilityLabel("Open menu")
      .accessibilityIdentifier("shell.menu")

      Text(title)
        .font(theme.font(.headline))
        .foregroundStyle(theme.text)

      Spacer()
    }
    .padding(.horizontal, 8)
    .padding(.vertical, 4)
    .background(theme.background)
    .overlay(alignment: .bottom) {
      Rectangle()
        .fill(theme.border)
        .frame(height: 1)
    }
  }
}

private struct AppDrawer: View {
  @Environment(GhostTheme.self) private var theme

  let selection: AppDestination
  let connected: Bool
  let onSelect: (AppDestination) -> Void
  let onLock: () -> Void

  var body: some View {
    ScrollView {
      LazyVStack(alignment: .leading, spacing: 0) {
        HStack(spacing: 12) {
          GhostMark(width: 32)
          Text("LOCALGHOST")
            .font(theme.font(.title2))
            .foregroundStyle(theme.accent)
        }

        HStack(spacing: 8) {
          Text(connected ? "●" : "○")
            .foregroundStyle(connected ? theme.accent : theme.textDim)
          Text(connected ? "connected to the box" : "not connected")
            .foregroundStyle(theme.textDim)
        }
        .font(theme.font(.caption))
        .padding(.top, 8)
        .padding(.bottom, 28)

        ForEach(AppDestination.primary) { row($0) }

        drawerSection("YOUR ARCHIVE", destinations: AppDestination.archive)
        drawerSection("THE BOX", destinations: AppDestination.box)

        Rectangle()
          .fill(theme.border)
          .frame(height: 1)
          .padding(.vertical, 16)

        ForEach(AppDestination.application) { row($0) }
        DrawerRow(glyph: "⏻", label: "LOCK", selected: false, action: onLock)
          .accessibilityIdentifier("shell.lock")
      }
      .padding(20)
    }
    .scrollIndicators(.hidden)
    .background(theme.background.ignoresSafeArea())
    .overlay(alignment: .trailing) {
      Rectangle()
        .fill(theme.border)
        .frame(width: 1)
    }
  }

  @ViewBuilder
  private func drawerSection(_ title: String, destinations: [AppDestination]) -> some View {
    Text(title)
      .font(theme.font(.caption))
      .foregroundStyle(theme.accentDim)
      .padding(.top, 20)
      .padding(.bottom, 6)
      .accessibilityAddTraits(.isHeader)

    ForEach(destinations) { row($0) }
  }

  private func row(_ destination: AppDestination) -> some View {
    DrawerRow(
      glyph: destination.glyph,
      label: destination.label,
      selected: destination == selection
    ) {
      onSelect(destination)
    }
    .accessibilityIdentifier("shell.destination.\(destination.rawValue)")
  }
}

private struct DrawerRow: View {
  @Environment(GhostTheme.self) private var theme

  let glyph: String
  let label: String
  let selected: Bool
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      HStack(spacing: 16) {
        Text(glyph)
          .font(theme.font(.title3))
          .frame(width: 36, alignment: .leading)
        Text(label)
          .font(theme.font(.body))
        Spacer()
      }
      .foregroundStyle(selected ? theme.accent : theme.text)
      .frame(minHeight: 52)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .accessibilityAddTraits(selected ? .isSelected : [])
  }
}

private struct PendingDestinationView: View {
  @Environment(GhostTheme.self) private var theme

  let destination: AppDestination

  var body: some View {
    GhostScreen {
      VStack(alignment: .leading, spacing: 14) {
        TerminalSectionLabel(text: destination.label)
        Text("This part of LocalGhost is not on iOS yet.")
          .font(theme.font(.body))
          .foregroundStyle(theme.textDim)
        Spacer()
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      .padding(20)
    }
  }
}

#Preview {
  AppShellView(
    boxModel: BoxModel(
      credentialStore: PreviewShellCredentialStore(),
      keyring: KeychainDeviceKeyring(),
      sessions: KeychainSessionStore(service: "ai.localghost.app.preview.session")
    ),
    onLock: {},
    onForgotten: {}
  )
  .environment(GhostTheme())
  .preferredColorScheme(.dark)
}

private struct PreviewShellCredentialStore: CredentialStoring {
  func load() throws -> EnrolmentCredentials? { nil }
  func save(_: EnrolmentCredentials) throws {}
  func clear() throws {}
}

