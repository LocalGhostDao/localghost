import Foundation
import Observation

nonisolated enum ChatRole: String, Sendable, Equatable {
  case user
  case assistant
}

nonisolated enum ChatMessageState: String, Sendable, Equatable {
  case complete
  case writing
  case stopped
  case failed
}

nonisolated enum ChatAnswerLength: String, CaseIterable, Sendable, Equatable {
  case short
  case medium
  case long

  var next: Self {
    switch self {
    case .short: .medium
    case .medium: .long
    case .long: .short
    }
  }

  var glyph: String {
    switch self {
    case .short: "▁"
    case .medium: "▃"
    case .long: "▆"
    }
  }
}

nonisolated struct ChatMessage: Identifiable, Sendable, Equatable {
  let id: String
  let serverID: Int64?
  let role: ChatRole
  var text: String
  var memories: [String]
  var reasoning: String
  var sources: [ChatSource]
  var status: String
  var steps: [String]
  var state: ChatMessageState
  let timestamp: Int64

  static func local(role: ChatRole, text: String, timestamp: Int64) -> Self {
    Self(
      id: UUID().uuidString,
      serverID: nil,
      role: role,
      text: text,
      memories: [],
      reasoning: "",
      sources: [],
      status: "",
      steps: [],
      state: .complete,
      timestamp: timestamp
    )
  }
}

/// Chat state lives only for the unlocked session. The box remains the source of truth for saved
/// conversations; this model deliberately has no phone-side transcript store.
@MainActor
@Observable
final class ChatModel {
  typealias ClientFactory = @MainActor @Sendable () throws -> any ChatCalling

  private(set) var messages: [ChatMessage] = []
  private(set) var conversations: [ChatSummary] = []
  /// Always the box's newest chats, independent of a search in the full history screen.
  private(set) var recentConversations: [ChatSummary] = []
  private(set) var activeChatID: Int64?
  private(set) var conversationQuery = ""
  private(set) var conversationsExhausted = false
  private(set) var olderMessagesExhausted = true
  private(set) var loadingConversations = false
  private(set) var loadingConversation = false
  private(set) var loadingOlderMessages = false
  private(set) var streaming = false
  private(set) var watchingWriting = false
  private(set) var errorMessage: String?
  private(set) var lastTouchedAt: Date?
  private(set) var tailRevision = 0
  private(set) var incognito = false
  private(set) var answerLength: ChatAnswerLength
  private(set) var conversationErrorMessage: String?

  var canSend: Bool { !streaming && !watchingWriting }
  var isGenerating: Bool { streaming || watchingWriting }
  var lastUserPrompt: String? {
    messages.last(where: { $0.role == .user })?.text
  }

  private let makeClient: ClientFactory
  private let preferences: UserDefaults
  private var streamTask: Task<Void, Never>?
  private var writingTask: Task<Void, Never>?
  private var stopTasks: [UUID: Task<Void, Never>] = [:]
  private var didStart = false
  private var streamGeneration = 0
  private var writingGeneration = 0
  private var conversationRequest = 0
  private var openRequest = 0
  private var sessionGeneration = 0

  init(
    client: @escaping ClientFactory,
    preferences: UserDefaults = .standard
  ) {
    makeClient = client
    self.preferences = preferences
    answerLength = ChatAnswerLength(
      rawValue: preferences.string(forKey: Self.answerLengthKey) ?? ""
    ) ?? .medium
  }

  func start() async {
    guard !didStart else { return }
    didStart = true
    if messages.isEmpty, !incognito {
      let lastChatID = Int64(preferences.integer(forKey: Self.lastChatIDKey))
      if lastChatID > 0 { await openConversation(lastChatID) }
    }
    await refreshConversations()
  }

  func refreshConversations(query: String = "") async {
    let query = String(query.trimmingCharacters(in: .whitespacesAndNewlines).prefix(120))
    conversationRequest += 1
    let request = conversationRequest
    let session = sessionGeneration
    conversationQuery = query
    loadingConversations = true
    errorMessage = nil
    do {
      let client = try makeClient()
      let page = try await client.chats(query: query, beforeUpdated: 0, limit: 20)
      guard request == conversationRequest, session == sessionGeneration else { return }
      conversations = page
      conversationsExhausted = page.count < 20
      if query.isEmpty { recentConversations = page }
      conversationErrorMessage = nil
    } catch is CancellationError {
      return
    } catch {
      guard request == conversationRequest, session == sessionGeneration else { return }
      conversationErrorMessage = Self.words(for: error)
    }
    if request == conversationRequest, session == sessionGeneration { loadingConversations = false }
  }

  func loadMoreConversations() async {
    guard !loadingConversations, !conversationsExhausted,
          let before = conversations.last?.updatedAt
    else { return }
    let request = conversationRequest
    let query = conversationQuery
    let session = sessionGeneration
    loadingConversations = true
    do {
      let client = try makeClient()
      let page = try await client.chats(query: query, beforeUpdated: before, limit: 20)
      guard request == conversationRequest,
            query == conversationQuery,
            session == sessionGeneration
      else { return }
      let known = Set(conversations.map(\.id))
      conversations.append(contentsOf: page.filter { !known.contains($0.id) })
      conversationsExhausted = page.count < 20
      if conversationQuery.isEmpty { recentConversations = conversations }
      conversationErrorMessage = nil
    } catch is CancellationError {
      return
    } catch {
      guard request == conversationRequest, session == sessionGeneration else { return }
      conversationErrorMessage = Self.words(for: error)
    }
    if request == conversationRequest, session == sessionGeneration { loadingConversations = false }
  }

  func openConversation(_ chatID: Int64) async {
    guard chatID > 0 else { return }
    if streaming || watchingWriting {
      cancelVisibleGenerationForReplacement()
    } else {
      abandonVisibleStream()
    }
    openRequest += 1
    let request = openRequest
    let session = sessionGeneration
    loadingOlderMessages = false
    loadingConversation = true
    errorMessage = nil
    do {
      let client = try makeClient()
      let newestFirst = try await client.chatMessages(chatID: chatID, beforeID: 0, limit: 100)
      guard request == openRequest, session == sessionGeneration else { return }
      messages = newestFirst.reversed().map(Self.message(from:))
      activeChatID = chatID
      preferences.set(chatID, forKey: Self.lastChatIDKey)
      incognito = false
      olderMessagesExhausted = newestFirst.count < 100
      tailRevision += 1
      if let newest = newestFirst.first,
         newest.role != ChatRole.user.rawValue,
         newest.state == ChatMessageState.writing.rawValue
      {
        watchWriting(chatID: chatID, messageID: newest.id)
      }
    } catch is CancellationError {
      return
    } catch {
      guard request == openRequest, session == sessionGeneration else { return }
      errorMessage = Self.words(for: error)
    }
    if request == openRequest, session == sessionGeneration { loadingConversation = false }
  }

  func loadOlderMessages() async {
    guard !loadingOlderMessages, !olderMessagesExhausted,
          let chatID = activeChatID,
          let before = messages.compactMap(\.serverID).min()
    else { return }
    let request = openRequest
    let session = sessionGeneration
    loadingOlderMessages = true
    do {
      let client = try makeClient()
      let newestFirst = try await client.chatMessages(chatID: chatID, beforeID: before, limit: 100)
      guard request == openRequest,
            session == sessionGeneration,
            activeChatID == chatID
      else { return }
      let known = Set(messages.compactMap(\.serverID))
      let older = newestFirst.reversed().filter { !known.contains($0.id) }.map(Self.message(from:))
      messages.insert(contentsOf: older, at: 0)
      olderMessagesExhausted = newestFirst.count < 100
    } catch is CancellationError {
      return
    } catch {
      guard request == openRequest, session == sessionGeneration else { return }
      errorMessage = Self.words(for: error)
    }
    if request == openRequest, session == sessionGeneration { loadingOlderMessages = false }
  }

  func newConversation() {
    if streaming || watchingWriting {
      cancelVisibleGenerationForReplacement()
    } else {
      abandonVisibleStream()
    }
    openRequest += 1
    loadingConversation = false
    loadingOlderMessages = false
    messages = []
    activeChatID = nil
    preferences.set(0, forKey: Self.lastChatIDKey)
    olderMessagesExhausted = true
    errorMessage = nil
    lastTouchedAt = nil
    tailRevision += 1
  }

  /// Matches Android: entering incognito detaches the current transcript from its saved chat, but
  /// keeps the visible turns as bounded context. Turning it off makes the next prompt a new chat.
  func toggleIncognito() {
    guard !isGenerating else { return }
    incognito.toggle()
    if incognito { activeChatID = nil }
  }

  func cycleAnswerLength() {
    answerLength = answerLength.next
    preferences.set(answerLength.rawValue, forKey: Self.answerLengthKey)
  }

  @discardableResult
  func send(_ rawPrompt: String) -> Bool {
    let prompt = rawPrompt.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !prompt.isEmpty else { return false }
    if streaming || watchingWriting { cancelVisibleGenerationForReplacement() }
    streamTask?.cancel()
    streamGeneration += 1
    let generation = streamGeneration
    let prior = Array(messages.suffix(12)).compactMap { message -> ChatHistoryTurn? in
      guard !message.text.isEmpty else { return nil }
      return ChatHistoryTurn(
        role: message.role.rawValue,
        content: String(message.text.prefix(4_000))
      )
    }
    let now = Int64(Date().timeIntervalSince1970 * 1_000)
    messages.append(.local(role: .user, text: prompt, timestamp: now))
    var answer = ChatMessage.local(role: .assistant, text: "", timestamp: now + 1)
    answer.status = "retrieving from index…"
    answer.state = .writing
    messages.append(answer)
    let answerID = answer.id
    let request = ChatRequest(
      prompt: prompt,
      think: "",
      incognito: incognito,
      chatId: incognito ? 0 : activeChatID ?? 0,
      history: prior,
      length: answerLength.rawValue
    )
    streaming = true
    errorMessage = nil
    lastTouchedAt = Date()
    tailRevision += 1
    streamTask = Task { [weak self] in
      await self?.runStream(request, answerID: answerID, generation: generation)
    }
    return true
  }

  func stop() {
    guard streaming || watchingWriting else { return }
    let chatID = activeChatID
    cancelVisibleGenerationForStop()
    guard !incognito, let chatID, chatID > 0 else { return }
    requestStop(chatID: chatID)
  }

  private func requestStop(chatID: Int64) {
    let token = UUID()
    let session = sessionGeneration
    let stoppedGeneration = streamGeneration
    stopTasks[token] = Task { [weak self] in
      guard let self else { return }
      defer { self.stopTasks[token] = nil }
      do {
        let client = try self.makeClient()
        _ = try await client.stopChat(chatID: chatID)
      } catch is CancellationError {
        return
      } catch {
        guard session == self.sessionGeneration,
              stoppedGeneration == self.streamGeneration,
              self.activeChatID == chatID
        else { return }
        self.errorMessage = "the box did not confirm that the answer stopped: \(Self.words(for: error))"
      }
    }
  }

  func renameConversation(_ chatID: Int64, to rawTitle: String) async {
    let title = rawTitle.trimmingCharacters(in: .whitespacesAndNewlines)
    guard chatID > 0, !title.isEmpty, title.count <= 200 else { return }
    let session = sessionGeneration
    do {
      let client = try makeClient()
      try await client.renameChat(chatID: chatID, title: title)
      guard session == sessionGeneration else { return }
      replaceConversation(chatID) { old in
        ChatSummary(
          id: old.id,
          title: title,
          createdAt: old.createdAt,
          updatedAt: old.updatedAt,
          messages: old.messages
        )
      }
      await refreshRecents()
    } catch is CancellationError {
      return
    } catch {
      guard session == sessionGeneration else { return }
      errorMessage = Self.words(for: error)
    }
  }

  func deleteConversation(_ chatID: Int64) async {
    guard chatID > 0 else { return }
    guard activeChatID != chatID || !isGenerating else {
      errorMessage = "stop the current answer before deleting this chat"
      return
    }
    let session = sessionGeneration
    do {
      let client = try makeClient()
      try await client.deleteChat(chatID: chatID)
      guard session == sessionGeneration else { return }
      conversations.removeAll { $0.id == chatID }
      recentConversations.removeAll { $0.id == chatID }
      if activeChatID == chatID { newConversation() }
    } catch is CancellationError {
      return
    } catch {
      guard session == sessionGeneration else { return }
      errorMessage = Self.words(for: error)
    }
  }

  /// Called at the app's lock boundary: no unlocked transcript or task survives in memory.
  func resetForLock() {
    sessionGeneration += 1
    conversationRequest += 1
    openRequest += 1
    abandonVisibleStream()
    stopTasks.values.forEach { $0.cancel() }
    stopTasks.removeAll()
    didStart = false
    messages = []
    conversations = []
    recentConversations = []
    activeChatID = nil
    conversationQuery = ""
    conversationsExhausted = false
    olderMessagesExhausted = true
    loadingConversations = false
    loadingConversation = false
    loadingOlderMessages = false
    errorMessage = nil
    conversationErrorMessage = nil
    lastTouchedAt = nil
    incognito = false
    tailRevision += 1
  }

  private func runStream(_ request: ChatRequest, answerID: String, generation: Int) async {
    var sawDone = false
    do {
      let client = try makeClient()
      for try await event in client.streamChat(request) {
        try Task.checkCancellation()
        guard generation == streamGeneration else { return }
        apply(event, to: answerID)
        if event == .done {
          sawDone = true
          break
        }
      }
      guard generation == streamGeneration else { return }
      finishStream(answerID: answerID, clean: sawDone)
      await refreshRecents()
    } catch is CancellationError {
      return
    } catch {
      guard generation == streamGeneration else { return }
      failStream(answerID: answerID, error: error)
    }
  }

  private func apply(_ event: ChatEvent, to answerID: String) {
    guard let index = messages.firstIndex(where: { $0.id == answerID }) else { return }
    switch event {
    case .memories(let memories):
      messages[index].memories = memories
    case .chatID(let id):
      guard !incognito else { break }
      activeChatID = id
      preferences.set(id, forKey: Self.lastChatIDKey)
    case .reasoning(let fragment):
      messages[index].reasoning += fragment
      messages[index].status = ""
    case .token(let fragment):
      messages[index].text += fragment
      messages[index].status = ""
    case .status(let status):
      appendStep(status, at: index)
      if messages[index].text.isEmpty, messages[index].reasoning.isEmpty {
        messages[index].status = status
      }
    case .steps(let steps):
      for step in steps { appendStep(step, at: index) }
    case .more(let queries, let why):
      let suffix = why.isEmpty ? "" : " (\(why))"
      let line = "the box asked for another web search\(suffix): \(queries.joined(separator: " · "))"
      appendStep(line, at: index)
      messages[index].status = ""
      messages[index].text = "This answer needs another web search, which is not available on iOS yet."
      messages[index].state = .failed
    case .done:
      break
    }
    tailRevision += 1
  }

  private func finishStream(answerID: String, clean: Bool) {
    guard let index = messages.firstIndex(where: { $0.id == answerID }) else { return }
    streaming = false
    streamTask = nil
    if messages[index].state != .failed {
      messages[index].state = clean ? .complete : .failed
    }
    messages[index].status = ""
    if messages[index].text.isEmpty, messages[index].reasoning.isEmpty {
      messages[index].text = clean
        ? "The box sent no answer. Box Status says whether the model is up."
        : "The answer stream ended before the box said it was done."
    }
    tailRevision += 1
  }

  private func failStream(answerID: String, error: Error) {
    streaming = false
    streamTask = nil
    errorMessage = Self.words(for: error)
    guard let index = messages.firstIndex(where: { $0.id == answerID }) else { return }
    messages[index].state = .failed
    messages[index].status = ""
    if messages[index].text.isEmpty, messages[index].reasoning.isEmpty {
      messages[index].text = "The box did not answer. It may be locked, or the model may still be loading. Check Box Status."
    }
    tailRevision += 1
  }

  private func watchWriting(chatID: Int64, messageID: Int64) {
    writingTask?.cancel()
    writingGeneration += 1
    let generation = writingGeneration
    let session = sessionGeneration
    watchingWriting = true
    writingTask = Task { [weak self] in
      guard let self else { return }
      while !Task.isCancelled {
        do {
          try await Task.sleep(for: .milliseconds(1_500))
          guard generation == self.writingGeneration,
                session == self.sessionGeneration,
                self.activeChatID == chatID,
                !self.streaming
          else { break }
          let client = try self.makeClient()
          let page = try await client.chatMessages(chatID: chatID, beforeID: 0, limit: 1)
          guard generation == self.writingGeneration, session == self.sessionGeneration else { break }
          guard let newest = page.first else { continue }
          guard newest.id == messageID else { break }
          if let index = self.messages.firstIndex(where: { $0.serverID == messageID }) {
            self.messages[index] = Self.message(from: newest)
            self.tailRevision += 1
          }
          if newest.state != ChatMessageState.writing.rawValue { break }
        } catch is CancellationError {
          break
        } catch {
          // A transient miss should not turn a still-running answer into a failure.
          continue
        }
      }
      if generation == self.writingGeneration, session == self.sessionGeneration {
        self.watchingWriting = false
        self.writingTask = nil
      }
    }
  }

  private func markLastAnswerStopped() {
    guard let index = messages.lastIndex(where: { $0.role == .assistant }) else { return }
    messages[index].state = .stopped
    messages[index].status = messages[index].text.isEmpty ? "stopped before the first word" : ""
    tailRevision += 1
  }

  private func appendStep(_ rawStep: String, at index: Int) {
    let step = rawStep.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !step.isEmpty, messages[index].steps.last != step else { return }
    messages[index].steps.append(step)
  }

  private func abandonVisibleStream() {
    streamGeneration += 1
    streamTask?.cancel()
    streamTask = nil
    writingTask?.cancel()
    writingTask = nil
    writingGeneration += 1
    streaming = false
    watchingWriting = false
  }

  private func cancelVisibleGenerationForStop() {
    streamGeneration += 1
    streamTask?.cancel()
    streamTask = nil
    writingTask?.cancel()
    writingTask = nil
    writingGeneration += 1
    streaming = false
    watchingWriting = false
    markLastAnswerStopped()
  }

  /// Android treats a new chat action as replacement, not a two-step stop-then-retry flow. The
  /// superseded answer remains visibly stopped when the replacement is another prompt; opening or
  /// clearing a conversation removes it immediately afterwards.
  private func cancelVisibleGenerationForReplacement() {
    abandonVisibleStream()
    markLastAnswerStopped()
  }

  private func refreshRecents() async {
    let session = sessionGeneration
    do {
      let client = try makeClient()
      let page = try await client.chats(query: "", beforeUpdated: 0, limit: 20)
      guard session == sessionGeneration else { return }
      recentConversations = page
      if conversationQuery.isEmpty {
        conversations = page
        conversationsExhausted = page.count < 20
      }
    } catch is CancellationError {
      return
    } catch {
      // The answer remains valid even if refreshing its drawer row did not.
    }
  }

  private func replaceConversation(_ id: Int64, transform: (ChatSummary) -> ChatSummary) {
    if let index = conversations.firstIndex(where: { $0.id == id }) {
      conversations[index] = transform(conversations[index])
    }
    if let index = recentConversations.firstIndex(where: { $0.id == id }) {
      recentConversations[index] = transform(recentConversations[index])
    }
  }

  private static func message(from saved: SavedChatMessage) -> ChatMessage {
    let role = saved.role == ChatRole.user.rawValue ? ChatRole.user : .assistant
    let state: ChatMessageState = switch saved.state {
    case ChatMessageState.writing.rawValue: .writing
    case ChatMessageState.stopped.rawValue: .stopped
    default: .complete
    }
    return ChatMessage(
      id: "server-\(saved.id)",
      serverID: saved.id,
      role: role,
      text: saved.content,
      memories: [],
      reasoning: saved.reasoning ?? "",
      sources: saved.sources ?? [],
      status: state == .writing ? "the box is still writing this answer…" : "",
      steps: [],
      state: state,
      timestamp: saved.ts
    )
  }

  private static func words(for error: Error) -> String {
    if let error = error as? LocalizedError, let description = error.errorDescription {
      return description
    }
    return error.localizedDescription
  }

  /// Forgetting the box: the chat last opened was that box's, and its id means another chat (or
  /// none) on the next one. How long an answer should be is this person's and stays.
  static func forgetLastChat(in preferences: UserDefaults = .standard) {
    preferences.removeObject(forKey: lastChatIDKey)
  }

  private static let answerLengthKey = "chat.answer-length"
  private static let lastChatIDKey = "chat.last-id"
}
