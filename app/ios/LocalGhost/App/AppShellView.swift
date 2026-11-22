import SwiftUI

enum AppDestination: String, CaseIterable, Identifiable {
  case home
  case chat
  case chats
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
    case .chats: "CHATS"
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
    case .chats: "◇"
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
  /// Routes reached from contextual navigation rather than a permanent drawer row.
  static let secondary: [Self] = [.chats]
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
  @State private var homeDetail: HomeLink?
  @State private var chatModel: ChatModel
  @State private var homeModel: HomeModel
  @State private var homeLocation: HomeLocationSource

  let boxModel: BoxModel
  let onLock: () -> Void
  let onForgotten: () -> Void

  init(
    boxModel: BoxModel,
    onLock: @escaping () -> Void,
    onForgotten: @escaping () -> Void
  ) {
    self.boxModel = boxModel
    self.onLock = onLock
    self.onForgotten = onForgotten
    let location = HomeLocationSource()
    _homeLocation = State(initialValue: location)
    _chatModel = State(initialValue: ChatModel(client: { try boxModel.authenticatedClient() }))
    _homeModel = State(initialValue: HomeModel(
      client: { try boxModel.authenticatedClient() },
      location: { location.latestAndRefresh() },
      cache: boxModel.homeCache
    ))
  }

  var body: some View {
    GeometryReader { proxy in
      let drawerWidth = min(360, proxy.size.width * 0.82)

      ZStack(alignment: .leading) {
        VStack(spacing: 0) {
          AppTopBar(
            title: homeDetail.map(homeDetailTitle) ?? destination.label,
            openMenu: { setDrawer(open: true) },
            goHome: homeDetail == nil && destination == .home ? nil : {
              homeDetail = nil
              destination = .home
            },
            chatModel: homeDetail == nil && destination == .chat ? chatModel : nil,
            onNewChat: {
              chatModel.newConversation()
              destination = .chat
            }
          )

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
          conversations: chatModel.recentConversations,
          activeChatID: chatModel.activeChatID,
          onSelect: select,
          onSelectConversation: openConversation,
          onNewConversation: {
            chatModel.newConversation()
            setDrawer(open: false)
          },
          onDeleteConversation: { id in
            Task { await chatModel.deleteConversation(id) }
          },
          onSeeAllChats: {
            homeDetail = nil
            destination = .chats
            setDrawer(open: false)
          },
          onLock: {
            setDrawer(open: false)
            chatModel.resetForLock()
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
    .task {
      homeLocation.start()
      await chatModel.start()
    }
    .onChange(of: homeLocation.latest) { old, new in
      guard old == nil, new != nil else { return }
      Task { await homeModel.refresh() }
    }
  }

  @ViewBuilder
  private var destinationView: some View {
    if let homeDetail {
      PendingFeatureView(label: homeDetailTitle(homeDetail))
    } else {
      switch destination {
      case .home:
        HomeView(
          model: homeModel,
          continuing: continuingPrompt,
          onAsk: askFromHome,
          onNewChat: { chatModel.newConversation() },
          onOpenChat: { destination = .chat },
          onOpen: openHomeLink
        )
      case .chat:
        ChatView(model: chatModel)
      case .chats:
        ChatHistoryView(
          model: chatModel,
          onOpenConversation: { destination = .chat },
          onNewConversation: { destination = .chat }
        )
      case .boxStatus:
        BoxStatusView(model: boxModel) {
          chatModel.resetForLock()
          onForgotten()
        }
      case .glossary:
        GlossaryView()
      case .about:
        AboutView()
      default:
        PendingDestinationView(destination: destination)
      }
    }
  }

  private var continuingPrompt: String {
    guard let touched = chatModel.lastTouchedAt,
          Date().timeIntervalSince(touched) <= 20 * 60
    else { return "" }
    return chatModel.lastUserPrompt ?? ""
  }

  private func askFromHome(_ question: String) -> Bool {
    if chatModel.messages.isEmpty
      || chatModel.lastTouchedAt.map({ Date().timeIntervalSince($0) > 20 * 60 }) != false
    {
      chatModel.newConversation()
    }
    guard chatModel.send(question) else { return false }
    destination = .chat
    return true
  }

  private func openConversation(_ id: Int64) {
    homeDetail = nil
    destination = .chat
    setDrawer(open: false)
    Task { await chatModel.openConversation(id) }
  }

  private func openHomeLink(_ link: HomeLink) {
    switch link {
    case .memories, .day, .memory:
      select(.memories)
    default:
      homeDetail = link
    }
  }

  private func homeDetailTitle(_ link: HomeLink) -> String {
    switch link {
    case .crypto, .coin: "CRYPTO"
    case .weather: "WEATHER"
    case .calendar: "CALENDAR"
    case .news, .story: "NEWS"
    case .memories, .day, .memory: "MEMORIES"
    }
  }

  private func select(_ newDestination: AppDestination) {
    homeDetail = nil
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
  let goHome: (() -> Void)?
  let chatModel: ChatModel?
  let onNewChat: () -> Void

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

      if let goHome {
        Button(action: goHome) { titleContent(showBack: true) }
          .buttonStyle(.plain)
          .accessibilityLabel("Back to Home")
      } else {
        titleContent(showBack: false)
      }

      Spacer()

      if let chatModel {
        Button { chatModel.toggleIncognito() } label: {
          VStack(spacing: 0) {
            Text(chatModel.incognito ? "◉" : "◎")
              .font(theme.font(.headline))
            Text(chatModel.incognito ? "incognito" : "saved")
              .font(theme.font(.caption2))
          }
          .foregroundStyle(chatModel.incognito ? theme.warning : theme.textDim)
        }
        .disabled(chatModel.isGenerating)
        .accessibilityLabel(chatModel.incognito ? "Incognito on: not saved" : "Incognito off: saved on the box")
        .accessibilityIdentifier("chat.incognito")

        Button { chatModel.cycleAnswerLength() } label: {
          VStack(spacing: 0) {
            Text(chatModel.answerLength.glyph)
              .font(theme.font(.headline))
            Text(chatModel.answerLength.rawValue)
              .font(theme.font(.caption2))
          }
          .foregroundStyle(chatModel.answerLength == .long ? theme.accent : theme.textDim)
        }
        .accessibilityLabel("Answer length: \(chatModel.answerLength.rawValue)")
        .accessibilityIdentifier("chat.answer-length")

        Button(action: onNewChat) {
          Text("＋")
            .font(theme.font(.title2))
            .foregroundStyle(theme.accent)
            .frame(width: 36, height: 44)
        }
        .accessibilityLabel("New chat")
        .accessibilityIdentifier("chat.new")
      }
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

  private func titleContent(showBack: Bool) -> some View {
    HStack(spacing: 8) {
      if showBack {
        Text("‹").foregroundStyle(theme.accent)
      }
      GhostMark(width: 22)
      Text(title)
        .font(theme.font(.headline))
        .foregroundStyle(theme.text)
        .lineLimit(1)
    }
  }
}

private struct AppDrawer: View {
  @Environment(GhostTheme.self) private var theme

  let selection: AppDestination
  let connected: Bool
  let conversations: [ChatSummary]
  let activeChatID: Int64?
  let onSelect: (AppDestination) -> Void
  let onSelectConversation: (Int64) -> Void
  let onNewConversation: () -> Void
  let onDeleteConversation: (Int64) -> Void
  let onSeeAllChats: () -> Void
  let onLock: () -> Void
  @State private var showMoreChats = false
  @State private var armedDeleteID: Int64?

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

        if !conversations.isEmpty {
          recentChats
        }

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

  private var recentChats: some View {
    VStack(alignment: .leading, spacing: 0) {
      HStack {
        Text("RECENT CHATS")
          .foregroundStyle(theme.accentDim)
        Spacer()
        Button("＋ new", action: onNewConversation)
          .foregroundStyle(theme.accent)
      }
      .font(theme.font(.caption))
      .padding(.top, 20)
      .padding(.bottom, 6)

      ForEach(conversations.prefix(showMoreChats ? 10 : 5)) { conversation in
        HStack(spacing: 8) {
          Button { onSelectConversation(conversation.id) } label: {
            VStack(alignment: .leading, spacing: 2) {
              Text(conversation.title.isEmpty ? "(untitled)" : conversation.title)
                .foregroundStyle(conversation.id == activeChatID ? theme.accent : theme.text)
                .lineLimit(1)
              Text("\(chatAge(conversation.updatedAt)) · \(conversation.messages) msgs")
                .font(theme.font(.caption2))
                .foregroundStyle(theme.textDim)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
          }
          .buttonStyle(.plain)

          Button(armedDeleteID == conversation.id ? "[ delete? ]" : "✕") {
            if armedDeleteID == conversation.id {
              armedDeleteID = nil
              onDeleteConversation(conversation.id)
            } else {
              armedDeleteID = conversation.id
            }
          }
          .font(theme.font(.caption2))
          .foregroundStyle(armedDeleteID == conversation.id ? theme.warning : theme.textDim)
          .buttonStyle(.plain)
        }
        .frame(minHeight: 48)
      }

      if !showMoreChats, conversations.count > 5 {
        Button("view more ▾") { showMoreChats = true }
          .foregroundStyle(theme.accentDim)
          .font(theme.font(.caption))
          .padding(.vertical, 4)
      } else if showMoreChats || conversations.count > 10 {
        Button("see all chats ›", action: onSeeAllChats)
          .foregroundStyle(theme.accentDim)
          .font(theme.font(.caption))
          .padding(.vertical, 4)
      }
    }
  }

  private func chatAge(_ milliseconds: Int64) -> String {
    let seconds = max(0, Int64(Date().timeIntervalSince1970) - milliseconds / 1_000)
    return switch seconds {
    case ..<90: "just now"
    case ..<3_600: "\(seconds / 60)m ago"
    case ..<86_400: "\(seconds / 3_600)h ago"
    default: "\(seconds / 86_400)d ago"
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
  let destination: AppDestination

  var body: some View {
    PendingFeatureView(label: destination.label)
  }
}

private struct PendingFeatureView: View {
  @Environment(GhostTheme.self) private var theme

  let label: String

  var body: some View {
    GhostScreen {
      VStack(alignment: .leading, spacing: 14) {
        TerminalSectionLabel(text: label)
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

