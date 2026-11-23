import SwiftUI
import UniformTypeIdentifiers

nonisolated func validatedChatSourceURL(_ raw: String) -> URL? {
  guard !raw.isEmpty,
        raw.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
        raw.rangeOfCharacter(from: .controlCharacters) == nil,
        !raw.contains("\\"),
        let components = URLComponents(string: raw),
        ["http", "https"].contains(components.scheme?.lowercased() ?? ""),
        let host = components.host,
        !host.isEmpty,
        host.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
        host.rangeOfCharacter(from: .controlCharacters) == nil,
        !host.contains("\\"),
        components.user == nil,
        components.password == nil
  else { return nil }
  return components.url
}
import UIKit

struct ChatView: View {
  private enum Notice: String, Identifiable {
    case attachment
    case microphone
    case model
    var id: Self { self }

    var title: String {
      switch self {
      case .attachment: "ADD TO CHAT"
      case .microphone: "ASK ALOUD"
      case .model: "ON-PHONE MODELS"
      }
    }

    var message: String {
      switch self {
      case .attachment: "Attachments are not on iOS yet. Nothing was added or sent."
      case .microphone: "Voice questions are not on iOS yet. Nothing was recorded or sent."
      case .model: "On-phone models are not on iOS yet. This chat uses synthd on your box."
      }
    }
  }

  @Environment(GhostTheme.self) private var theme
  @Environment(\.accessibilityReduceMotion) private var reduceMotion
  @State private var draft = ""
  @State private var notice: Notice?
  @State private var followTail = true

  let model: ChatModel

  var body: some View {
    GhostScreen {
      VStack(spacing: 0) {
        if model.incognito {
          Text("◉ incognito , this conversation is not saved anywhere")
            .font(theme.font(.caption))
            .foregroundStyle(theme.warning)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
        }
        if let error = model.errorMessage {
          GhostErrorBanner(message: error)
            .padding(.horizontal, 12)
            .padding(.top, 6)
        }
        transcript
      }
      .safeAreaInset(edge: .bottom, spacing: 0) { composer }
    }
    .task { await model.start() }
    .alert(item: $notice) { item in
      Alert(
        title: Text(item.title),
        message: Text(item.message),
        dismissButton: .default(Text("OK"))
      )
    }
  }

  private var transcript: some View {
    ScrollViewReader { proxy in
      ZStack(alignment: .bottomTrailing) {
        if model.messages.isEmpty, !model.loadingConversation {
          ChatEmptyState { _ = model.send($0) }
        } else {
          ScrollView {
            LazyVStack(alignment: .leading, spacing: 10) {
              if !model.olderMessagesExhausted {
                Button(model.loadingOlderMessages ? "loading…" : "LOAD EARLIER MESSAGES ▴") {
                  Task { await model.loadOlderMessages() }
                }
                .disabled(model.loadingOlderMessages)
                .font(theme.font(.caption))
                .foregroundStyle(theme.accentDim)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 8)
              }
              ForEach(model.messages) { message in
                ChatMessageView(
                  message: message,
                  selectable: !(model.streaming && message.id == model.messages.last?.id)
                )
              }
              Color.clear
                .frame(height: 1)
                .id("chat-tail")
                .onAppear { followTail = true }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
          }
          .scrollDismissesKeyboard(.interactively)
          .simultaneousGesture(
            DragGesture(minimumDistance: 8)
              .onChanged { _ in followTail = false }
          )
        }

        if model.loadingConversation {
          ProgressView()
            .tint(theme.accent)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(theme.background.opacity(0.82))
        }

        if !followTail, !model.messages.isEmpty {
          Button {
            followTail = true
            withAnimation((reduceMotion || theme.preferences.reduceMotion) ? nil : .easeOut(duration: 0.18)) {
              proxy.scrollTo("chat-tail", anchor: .bottom)
            }
          } label: {
            Text("↓ LATEST")
              .font(theme.font(.caption))
              .foregroundStyle(theme.accent)
              .padding(.horizontal, 10)
              .frame(minHeight: 36)
              .background(theme.surface)
              .overlay { Rectangle().stroke(theme.accent, lineWidth: 1) }
          }
          .padding(12)
        }
      }
      .onChange(of: model.tailRevision) { _, _ in
        guard followTail, !model.messages.isEmpty else { return }
        DispatchQueue.main.async { proxy.scrollTo("chat-tail", anchor: .bottom) }
      }
    }
  }

  private var composer: some View {
    VStack(alignment: .leading, spacing: 4) {
      Button { notice = .model } label: {
        HStack(spacing: 6) {
          Text("▪").foregroundStyle(theme.accent)
          Text("the box · synthd")
          Text("▾").foregroundStyle(theme.textDim)
        }
        .font(theme.font(.caption))
        .foregroundStyle(theme.text)
        .padding(.horizontal, 12)
        .frame(minHeight: 30)
        .overlay { Capsule().stroke(theme.textDim, lineWidth: 1) }
      }
      .accessibilityLabel("Model: the box, synthd")

      HStack(alignment: .bottom, spacing: 4) {
        unavailableButton("+", item: .attachment, label: "Add to chat")
        TextField("query your index…", text: $draft, axis: .vertical)
          .font(theme.font(.body))
          .foregroundStyle(theme.text)
          .tint(theme.accent)
          .lineLimit(1...5)
          .submitLabel(.send)
          .onSubmit { submit() }
          .padding(.horizontal, 6)
          .padding(.vertical, 9)
          .accessibilityIdentifier("chat.composer")
        unavailableButton("●", item: .microphone, label: "Ask aloud")
        Button {
          model.isGenerating ? model.stop() : submit()
        } label: {
          Text(model.isGenerating ? "■" : "↑")
            .font(theme.font(.headline))
            .foregroundStyle(theme.background)
            .frame(width: 40, height: 40)
            .background(actionable ? theme.accent : theme.border)
            .clipShape(Circle())
        }
        .disabled(!actionable)
        .accessibilityLabel(model.isGenerating ? "Stop answer" : "Send")
        .accessibilityIdentifier(model.isGenerating ? "chat.stop" : "chat.send")
      }
    }
    .padding(.horizontal, 8)
    .padding(.vertical, 6)
    .background(theme.surface)
    .clipShape(RoundedRectangle(cornerRadius: 22))
    .overlay { RoundedRectangle(cornerRadius: 22).stroke(theme.border, lineWidth: 1) }
    .padding(.horizontal, 8)
    .padding(.bottom, 4)
    .background(theme.background)
  }

  private var actionable: Bool {
    model.isGenerating || (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && model.canSend)
  }

  private func unavailableButton(_ glyph: String, item: Notice, label: String) -> some View {
    Button { notice = item } label: {
      Text(glyph)
        .font(theme.font(.title3))
        .foregroundStyle(theme.accent)
        .frame(width: 40, height: 40)
    }
    .accessibilityLabel(label)
  }

  private func submit() {
    let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty, model.canSend else { return }
    draft = ""
    followTail = true
    model.send(text)
  }
}

private struct ChatEmptyState: View {
  @Environment(GhostTheme.self) private var theme
  let ask: (String) -> Void
  private let examples = [
    "what did I do in Rome?",
    "summarise last week",
    "what's the weather like?",
    "what is a quasar?",
  ]

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 10) {
        TerminalSectionLabel(text: "GHOST.SYNTHD")
        Text("Local model, on the box.")
          .font(theme.font(.headline))
          .foregroundStyle(theme.accent)
        Text(
          "Runs on your box. Answers from your memories, your photos, voice notes and trail, " +
            "the box's own Wikipedia and news. The prompt and the index never leave the box."
        )
        .font(theme.font(.body))
        .foregroundStyle(theme.textDim)
        Text("try one")
          .font(theme.font(.caption))
          .foregroundStyle(theme.accentDim)
          .padding(.top, 8)
        ForEach(examples, id: \.self) { example in
          Button { ask(example) } label: {
            Text("› \(example)")
              .font(theme.font(.body))
              .foregroundStyle(theme.accent)
              .frame(maxWidth: .infinity, alignment: .leading)
              .padding(.vertical, 4)
          }
          .buttonStyle(.plain)
        }
      }
      .padding(.horizontal, 24)
      .padding(.top, 40)
    }
  }
}

private struct ChatMessageView: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.openURL) private var openURL
  @State private var memoriesOpen = false
  @State private var reasoningOpen = false
  @State private var sourcesOpen = false
  @State private var stepsOpen = false

  let message: ChatMessage
  let selectable: Bool

  var body: some View {
    VStack(alignment: message.role == .user ? .trailing : .leading, spacing: 4) {
      if !message.memories.isEmpty {
        disclosureButton(open: memoriesOpen, title: "\(message.memories.count) memories", accent: true) {
          memoriesOpen.toggle()
        }
        if memoriesOpen { disclosureLines(message.memories, glyph: "◇") }
      }

      if !message.sources.isEmpty {
        disclosureButton(open: sourcesOpen, title: "\(message.sources.count) from the web · searched on this phone", accent: true) {
          sourcesOpen.toggle()
        }
        if sourcesOpen {
          VStack(alignment: .leading, spacing: 5) {
            ForEach(message.sources) { source in
              Button {
                guard let url = safeWebURL(source.url) else { return }
                openURL(url)
              } label: {
                let number = source.n ?? ((message.sources.firstIndex(of: source) ?? 0) + 1)
                let metadata = [
                  safeWebURL(source.url)?.host(),
                  source.published.map { String($0.prefix(10)) },
                ].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
                let body = (source.excerpt?.isEmpty == false ? source.excerpt : source.snippet) ?? ""
                VStack(alignment: .leading, spacing: 2) {
                  Text("[\(number)] \(sourceGlyph(source.kind)) \(source.title)")
                    .foregroundStyle(theme.text)
                  if !metadata.isEmpty {
                    Text(metadata).foregroundStyle(theme.accentDim)
                  }
                  if !body.isEmpty {
                    Text(String(body.prefix(220)) + (body.count > 220 ? "…" : ""))
                      .foregroundStyle(theme.textDim)
                  }
                }
                .font(theme.font(.caption))
                .multilineTextAlignment(.leading)
                .frame(maxWidth: .infinity, alignment: .leading)
              }
              .buttonStyle(.plain)
              .disabled(safeWebURL(source.url) == nil)
            }
          }
          .padding(.leading, 18)
          .padding(.bottom, 4)
        }
      }

      if !message.status.isEmpty, message.text.isEmpty {
        VStack(alignment: .leading, spacing: 2) {
          ForEach(message.steps.suffix(4).dropLast(), id: \.self) { step in
            Text("› \(step)").foregroundStyle(theme.border)
          }
          Text("› \(message.status)").foregroundStyle(theme.textDim)
        }
        .font(theme.font(.caption))
        .padding(.vertical, 4)
      }

      if !message.reasoning.isEmpty {
        disclosureButton(
          open: reasoningOpen,
          title: "thinking… (\(message.reasoning.count))",
          accent: false
        ) { reasoningOpen.toggle() }
        if reasoningOpen {
          Text(message.reasoning)
            .font(theme.font(.caption))
            .foregroundStyle(theme.textDim)
            .padding(8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(theme.surface)
            .overlay { Rectangle().stroke(theme.border, lineWidth: 1) }
            .padding(.leading, 18)
        }
      }

      if !message.text.isEmpty {
        messageText
        if message.state == .stopped {
          Text("*(stopped)*")
            .font(theme.font(.caption))
            .foregroundStyle(theme.textDim)
        }
      }

      if message.role == .assistant, !message.steps.isEmpty, !message.text.isEmpty {
        disclosureButton(
          open: stepsOpen,
          title: "how (\(message.steps.count) step\(message.steps.count == 1 ? "" : "s"))",
          accent: false
        ) { stepsOpen.toggle() }
        if stepsOpen { disclosureLines(message.steps, glyph: "›") }
      }

      if message.role == .assistant, !message.text.isEmpty, selectable {
        Button("[ copy ]") { copyAnswer() }
          .buttonStyle(.plain)
          .font(theme.font(.caption2))
          .foregroundStyle(theme.textDim)
          .padding(.vertical, 2)
      }
    }
    .frame(maxWidth: .infinity, alignment: message.role == .user ? .trailing : .leading)
  }

  @ViewBuilder
  private var messageText: some View {
    let text = renderedText
      .font(theme.font(.body))
      .foregroundStyle(theme.text)
      .tint(theme.accent)
      .padding(
        message.role == .user
          ? EdgeInsets(top: 8, leading: 10, bottom: 8, trailing: 10)
          : EdgeInsets(top: 2, leading: 2, bottom: 2, trailing: 2)
      )
      .background(message.role == .user ? theme.surface : Color.clear)
      .overlay {
        if message.role == .user { Rectangle().stroke(theme.border, lineWidth: 1) }
      }

    if selectable { text.textSelection(.enabled) } else { text }
  }

  private var renderedText: Text {
    guard message.role == .assistant,
          var attributed = try? AttributedString(
            markdown: message.text,
            options: .init(interpretedSyntax: .full)
          )
    else { return Text(message.text) }
    // Android renders answer links as text; only validated source rows are actionable. Do the same
    // here so model-generated Markdown cannot open a non-web or credential-bearing URL.
    for run in attributed.runs where run.link != nil {
      attributed[run.range].link = nil
    }
    return Text(attributed)
  }

  private func disclosureButton(
    open: Bool,
    title: String,
    accent: Bool,
    action: @escaping () -> Void
  ) -> some View {
    Button(action: action) {
      HStack(spacing: 6) {
        Text(open ? "−" : "+")
        Text(title)
      }
      .font(theme.font(.caption))
      .foregroundStyle(accent ? theme.accent : theme.textDim)
      .padding(.vertical, 2)
    }
    .buttonStyle(.plain)
  }

  private func disclosureLines(_ lines: [String], glyph: String) -> some View {
    VStack(alignment: .leading, spacing: 2) {
      ForEach(lines, id: \.self) { line in
        Text("\(glyph) \(line)")
          .font(theme.font(.caption))
          .foregroundStyle(theme.textDim)
          .frame(maxWidth: .infinity, alignment: .leading)
      }
    }
    .padding(.leading, 18)
    .padding(.bottom, 4)
  }

  private func sourceGlyph(_ kind: String?) -> String {
    switch kind {
    case "weather": "☂"
    case "rate": "€"
    case "summary": "W"
    default: "◇"
    }
  }

  private func safeWebURL(_ raw: String) -> URL? {
    validatedChatSourceURL(raw)
  }

  private func copyAnswer() {
    UIPasteboard.general.setItems(
      [[UTType.utf8PlainText.identifier: message.text]],
      options: [
        .localOnly: true,
        .expirationDate: Date().addingTimeInterval(10 * 60),
      ]
    )
  }
}

struct ChatHistoryView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var query = ""
  @State private var editingID: Int64?
  @State private var editedTitle = ""
  @State private var deleteArmedID: Int64?

  let model: ChatModel
  let onOpenConversation: () -> Void
  let onNewConversation: () -> Void

  var body: some View {
    GhostScreen {
      VStack(spacing: 12) {
        HStack {
          TerminalSectionLabel(text: "CHATS")
          Spacer()
          Button("＋ NEW") {
            model.newConversation()
            onNewConversation()
          }
        }
        .font(theme.font(.caption))
        .foregroundStyle(theme.accent)

        searchField

        if model.loadingConversations, model.conversations.isEmpty {
          ProgressView()
            .tint(theme.accent)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if model.conversations.isEmpty {
          Text(model.conversationErrorMessage.map { "! The box did not answer: \($0)" } ?? emptyWords)
            .font(theme.font(.body))
            .foregroundStyle(model.conversationErrorMessage == nil ? theme.textDim : theme.warning)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .padding(.top, 8)
        } else {
          VStack(spacing: 6) {
            if let error = model.conversationErrorMessage {
              Text("! The box did not answer: \(error)")
                .font(theme.font(.caption))
                .foregroundStyle(theme.warning)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            ScrollView {
              LazyVStack(spacing: 8) {
              ForEach(model.conversations) { conversation in
                conversationRow(conversation)
              }
              if !model.conversationsExhausted {
                Button(model.loadingConversations ? "loading…" : "LOAD MORE ▾") {
                  Task { await model.loadMoreConversations() }
                }
                .disabled(model.loadingConversations)
                .font(theme.font(.caption))
                .foregroundStyle(theme.accentDim)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 16)
              }
              }
            }
            .scrollDismissesKeyboard(.interactively)
          }
        }
      }
      .padding(20)
    }
    .task(id: query) {
      do {
        try await Task.sleep(for: .milliseconds(350))
        try Task.checkCancellation()
        await model.refreshConversations(query: query)
      } catch {
        return
      }
    }
  }

  private var searchField: some View {
    HStack(spacing: 8) {
      Text("⌕").foregroundStyle(theme.textDim)
      TextField("search chats…", text: $query)
        .font(theme.font(.body))
        .foregroundStyle(theme.text)
        .tint(theme.accent)
        .textInputAutocapitalization(.never)
        .autocorrectionDisabled()
      if !query.isEmpty {
        Button("✕") { query = "" }
          .foregroundStyle(theme.textDim)
      }
    }
    .padding(.horizontal, 14)
    .frame(minHeight: 44)
    .background(theme.surface)
    .overlay { RoundedRectangle(cornerRadius: 20).stroke(theme.border, lineWidth: 1) }
  }

  private var emptyWords: String {
    let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
    return trimmed.isEmpty
      ? "no chats yet. start one with ＋ new , non-incognito chats land here."
      : "nothing on the box matches \"\(trimmed)\"."
  }

  @ViewBuilder
  private func conversationRow(_ conversation: ChatSummary) -> some View {
    HStack(alignment: .center, spacing: 8) {
      if editingID == conversation.id {
        VStack(alignment: .leading, spacing: 3) {
          TextField("chat title", text: $editedTitle)
            .font(theme.font(.body))
            .foregroundStyle(theme.accent)
            .tint(theme.accent)
            .textInputAutocapitalization(.sentences)
          Text(timestamp(conversation.updatedAt) + " · \(conversation.messages) msgs")
            .font(theme.font(.caption))
            .foregroundStyle(theme.textDim)
        }
        .frame(maxWidth: .infinity, alignment: .leading)

        Button("[ save ]") {
          let title = editedTitle
          editingID = nil
          Task { await model.renameConversation(conversation.id, to: title) }
        }
        .disabled(editedTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        Button("[ x ]") { editingID = nil }
      } else {
        Button {
          Task {
            await model.openConversation(conversation.id)
            if model.activeChatID == conversation.id { onOpenConversation() }
          }
        } label: {
          VStack(alignment: .leading, spacing: 3) {
            Text(conversation.title.isEmpty ? "(untitled)" : conversation.title)
              .font(theme.font(.body))
              .foregroundStyle(model.activeChatID == conversation.id ? theme.accent : theme.text)
              .lineLimit(1)
            Text(timestamp(conversation.updatedAt) + " · \(conversation.messages) msgs")
              .font(theme.font(.caption))
              .foregroundStyle(theme.textDim)
          }
          .frame(maxWidth: .infinity, alignment: .leading)
          .contentShape(Rectangle())
        }
        .buttonStyle(.plain)

        if deleteArmedID == conversation.id {
          Button("[ delete? ]") { armOrDelete(conversation.id) }
            .foregroundStyle(theme.warning)
          Button("[ no ]") { deleteArmedID = nil }
        } else {
          Button("✎") {
            editingID = conversation.id
            editedTitle = conversation.title
            deleteArmedID = nil
          }
          Button("✕") { armOrDelete(conversation.id) }
          Text("›").foregroundStyle(theme.accentDim)
        }
      }
    }
    .buttonStyle(.plain)
    .font(theme.font(.caption))
    .foregroundStyle(theme.textDim)
    .padding(14)
    .background(model.activeChatID == conversation.id ? theme.surface : theme.background)
    .overlay {
      Rectangle().stroke(
        model.activeChatID == conversation.id ? theme.accentDim : theme.border,
        lineWidth: 1
      )
    }
  }

  private func armOrDelete(_ id: Int64) {
    guard deleteArmedID == id else {
      deleteArmedID = id
      Task {
        try? await Task.sleep(for: .seconds(3))
        if deleteArmedID == id { deleteArmedID = nil }
      }
      return
    }
    deleteArmedID = nil
    Task { await model.deleteConversation(id) }
  }

  private func timestamp(_ rawTimestamp: Int64) -> String {
    let seconds = rawTimestamp > 10_000_000_000 ? rawTimestamp / 1_000 : rawTimestamp
    return Self.timestampFormatter.string(from: Date(timeIntervalSince1970: TimeInterval(seconds)))
  }

  private static let timestampFormatter: DateFormatter = {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_GB")
    formatter.calendar = Calendar(identifier: .gregorian)
    formatter.dateFormat = "d MMM, HH:mm"
    return formatter
  }()
}
