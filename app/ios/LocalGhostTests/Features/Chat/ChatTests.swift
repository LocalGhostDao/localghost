import Foundation
import Testing

@testable import LocalGhost

struct ChatSSETests {
  @Test
  func contextCarriesMemoriesStatusStepsAndPersistedID() {
    let line = #"data: {"context":[{"when":"last April","source":"photos","snippet":"dinner by the river"},{"source":"archive","snippet":""}],"note":"reading your archive","steps":["searched memories","read 3 matches"],"chatId":42}"#

    #expect(ChatSSE.events(from: line) == [
      .memories(["last April , photos: dinner by the river"]),
      .status("reading your archive"),
      .steps(["searched memories", "read 3 matches"]),
      .chatID(42),
    ])
  }

  @Test
  func tokenReasoningAndDoneAreDistinctEvents() {
    #expect(ChatSSE.events(from: #"data: {"r":"checking dates"}"#) == [.reasoning("checking dates")])
    #expect(ChatSSE.events(from: #"data: {"t":"The answer"}"#) == [.token("The answer")])
    #expect(ChatSSE.events(from: #"data: {"done":true,"chatId":7,"more":true}"#) == [.chatID(7), .done])
  }

  @Test
  func anotherSearchRoundIsDecodedWithoutConfusingDoneFlag() {
    let line = #"data: {"more":{"queries":["first query","second query"],"why":"the findings were thin"}}"#
    #expect(ChatSSE.events(from: line) == [
      .more(queries: ["first query", "second query"], why: "the findings were thin")
    ])
  }

  @Test
  func malformedAndNonDataLinesAreIgnored() {
    #expect(ChatSSE.events(from: "event: message").isEmpty)
    #expect(ChatSSE.events(from: "data:").isEmpty)
    #expect(ChatSSE.events(from: "data: not-json").isEmpty)
  }
}

struct ChatWireModelTests {
  @Test
  func savedMessageCarriesWritingStateAndSources() throws {
    let json = #"{"id":9,"role":"assistant","content":"half an answer","ts":1234,"reasoning":"checking","sources":[{"n":1,"title":"Source","url":"https://example.test","kind":"page","snippet":"search result","excerpt":"page body","source":"duckduckgo","published":"2026-11-18T09:00:00Z"}],"state":"writing"}"#
    let message = try JSONDecoder().decode(SavedChatMessage.self, from: Data(json.utf8))

    #expect(message.id == 9)
    #expect(message.state == "writing")
    #expect(message.reasoning == "checking")
    #expect(message.sources?.first?.title == "Source")
    #expect(message.sources?.first?.excerpt == "page body")
    #expect(message.sources?.first?.published == "2026-11-18T09:00:00Z")
  }

  @Test
  func requestUsesTheServerFieldNames() throws {
    let request = ChatRequest(
      prompt: "where was I?",
      think: "off",
      incognito: false,
      chatId: 17,
      history: [.init(role: "user", content: "before")],
      length: "medium"
    )
    let object = try #require(
      JSONSerialization.jsonObject(with: JSONEncoder().encode(request)) as? [String: Any]
    )

    #expect(object["chatId"] as? Int == 17)
    #expect(object["incognito"] as? Bool == false)
    #expect((object["history"] as? [[String: Any]])?.first?["content"] as? String == "before")
  }
}

struct ChatSourceURLTests {
  @Test(arguments: [
    "https://example.com%0d%0a.evil",
    "https://example.com%09.evil",
    "https://example.com%20.evil",
    "https://example.com%5c.evil",
    "https://example.com%00.evil",
  ])
  func rejectsEncodedAuthoritySeparators(_ raw: String) {
    #expect(validatedChatSourceURL(raw) == nil)
  }

  @Test
  func acceptsCredentialFreeWebSources() {
    #expect(validatedChatSourceURL("https://example.com/story?q=localghost") != nil)
    #expect(validatedChatSourceURL("http://127.0.0.1:8080/source") != nil)
    #expect(validatedChatSourceURL("https://user@example.com/") == nil)
  }
}

@MainActor
struct ChatAnswerLengthTests {
  @Test
  func settingsDefaultsArePersistedAndUsedByTheNextQuestion() async throws {
    let suite = "localghost-chat-settings-tests-\(UUID().uuidString)"
    let preferences = try #require(UserDefaults(suiteName: suite))
    defer { preferences.removePersistentDomain(forName: suite) }
    let client = RequestRecordingChatClient()
    let model = ChatModel(client: { client }, preferences: preferences)
    #expect(model.thinking == .direct)
    model.setThinking(.deep)
    model.setAnswerLength(.short)
    #expect(model.send("what should I focus on?"))
    let request = await client.waitForRequest()
    #expect(request.think == "deep")
    #expect(request.length == "short")
    let restored = ChatModel(client: { client }, preferences: preferences)
    #expect(restored.thinking == .deep)
    #expect(restored.answerLength == .short)
  }

  @Test
  func answerLengthCyclesPersistsAndRidesTheRequest() async throws {
    let suite = "localghost-chat-length-tests-\(UUID().uuidString)"
    let preferences = try #require(UserDefaults(suiteName: suite))
    defer { preferences.removePersistentDomain(forName: suite) }
    let client = RequestRecordingChatClient()
    let model = ChatModel(client: { client }, preferences: preferences)

    #expect(model.answerLength == .medium)
    model.cycleAnswerLength()
    #expect(model.answerLength == .long)
    #expect(model.send("tell me the whole story"))

    var request: ChatRequest?
    for _ in 0..<100 where request == nil {
      request = await client.requested()
      if request == nil { await Task.yield() }
    }
    #expect(request?.length == "long")

    let restored = ChatModel(client: { EmptyChatClient() }, preferences: preferences)
    #expect(restored.answerLength == .long)
  }
}

@MainActor
struct ChatModelLifecycleTests {
  @Test
  func lockInvalidatesAnInFlightHistoryRefresh() async {
    let gate = TestGate()
    let client = BlockingHistoryClient(gate: gate)
    let model = ChatModel(client: { client })
    let load = Task { await model.refreshConversations() }
    await gate.waitUntilBlocked()

    model.resetForLock()
    await gate.release()
    await load.value

    #expect(model.conversations.isEmpty)
    #expect(model.recentConversations.isEmpty)
  }

  @Test
  func newConversationPreservesIncognito() {
    let model = ChatModel(client: { EmptyChatClient() })

    model.toggleIncognito()
    model.newConversation()

    #expect(model.incognito)
  }

  @Test
  func newConversationClearsAnInvalidatedOpenProgressState() async {
    let gate = TestGate()
    let client = BlockingMessagesClient(gate: gate)
    let model = ChatModel(client: { client })
    let open = Task { await model.openConversation(9) }
    await gate.waitUntilBlocked()
    #expect(model.loadingConversation)

    model.newConversation()
    #expect(!model.loadingConversation)

    await gate.release()
    await open.value
    #expect(!model.loadingConversation)
    #expect(model.messages.isEmpty)
  }

  @Test
  func replacingAnActiveAnswerDoesNotRequireASecondAction() async {
    let client = ReplacementChatClient()
    let model = ChatModel(client: { client })
    #expect(model.send("first"))
    #expect(model.isGenerating)

    #expect(model.send("second"))

    #expect(model.messages.count == 4)
    #expect(model.messages[1].state == .stopped)
    #expect(model.messages[2].text == "second")
    #expect(model.messages[3].state == .writing)
    #expect(model.isGenerating)
  }

  @Test
  func openingAChatCancelsTheActiveAnswerAndOpensImmediately() async {
    let client = ReplacementChatClient()
    let model = ChatModel(client: { client })
    #expect(model.send("first"))

    await model.openConversation(41)

    #expect(model.activeChatID == 41)
    #expect(model.messages.map(\.text) == ["saved question"])
    #expect(!model.isGenerating)
  }

  @Test
  func newChatCancelsTheActiveAnswerAndClearsImmediately() {
    let model = ChatModel(client: { ReplacementChatClient() })
    #expect(model.send("first"))

    model.newConversation()

    #expect(model.messages.isEmpty)
    #expect(model.activeChatID == nil)
    #expect(!model.isGenerating)
  }

  @Test
  func lastSavedChatRestoresAndNewChatClearsThatContinuity() async throws {
    let suite = "localghost-chat-continuity-tests-\(UUID().uuidString)"
    let preferences = try #require(UserDefaults(suiteName: suite))
    defer { preferences.removePersistentDomain(forName: suite) }
    let client = ReplacementChatClient()

    let firstSession = ChatModel(client: { client }, preferences: preferences)
    await firstSession.openConversation(41)

    let restoredSession = ChatModel(client: { client }, preferences: preferences)
    await restoredSession.start()
    #expect(restoredSession.activeChatID == 41)
    #expect(restoredSession.messages.map(\.text) == ["saved question"])

    restoredSession.newConversation()
    let clearedSession = ChatModel(client: { client }, preferences: preferences)
    await clearedSession.start()
    #expect(clearedSession.activeChatID == nil)
    #expect(clearedSession.messages.isEmpty)
  }

  @Test
  func adoptedServerChatIDIsRestoredInTheNextSession() async throws {
    let suite = "localghost-chat-adoption-tests-\(UUID().uuidString)"
    let preferences = try #require(UserDefaults(suiteName: suite))
    defer { preferences.removePersistentDomain(forName: suite) }
    let client = AdoptedChatClient()
    let firstSession = ChatModel(client: { client }, preferences: preferences)

    #expect(firstSession.send("remember this"))
    await eventually { firstSession.activeChatID == 73 }

    let restoredSession = ChatModel(client: { client }, preferences: preferences)
    await restoredSession.start()
    #expect(restoredSession.activeChatID == 73)
    #expect(restoredSession.messages.map(\.text) == ["saved question"])
  }

  @Test
  func aSlowStopAcknowledgementDoesNotBlockTheNextPrompt() async {
    let gate = TestGate()
    let client = StoppableChatClient(stopGate: gate)
    let model = ChatModel(client: { client })
    #expect(model.send("first"))
    await eventually { model.activeChatID == 7 }

    model.stop()
    await gate.waitUntilBlocked()

    #expect(!model.isGenerating)
    #expect(model.canSend)
    #expect(model.send("second"))
    #expect(model.messages.last(where: { $0.role == .user })?.text == "second")

    await gate.release()
  }

  @Test
  func stoppingBeforeTheServerIDIsKnownRemainsAnImmediateLocalStop() async {
    let gate = TestGate()
    let client = DeferredIDChatClient(stopGate: gate)
    let model = ChatModel(client: { client })
    #expect(model.send("first"))

    model.stop()
    #expect(!model.isGenerating)
    #expect(model.canSend)

    client.emit(.chatID(11))
    for _ in 0..<10 { await Task.yield() }

    #expect(await client.stoppedChatID() == nil)
    #expect(model.activeChatID == nil)
    #expect(model.send("second"))
  }

  private func eventually(_ condition: @escaping @MainActor () -> Bool) async {
    for _ in 0..<100 where !condition() { await Task.yield() }
    #expect(condition())
  }
}

private actor TestGate {
  private var open = false
  private var blocked = false
  private var waiters: [CheckedContinuation<Void, Never>] = []
  private var observers: [CheckedContinuation<Void, Never>] = []

  func block() async {
    blocked = true
    let currentObservers = observers
    observers.removeAll()
    currentObservers.forEach { $0.resume() }
    guard !open else { return }
    await withCheckedContinuation { waiters.append($0) }
  }

  func waitUntilBlocked() async {
    guard !blocked else { return }
    await withCheckedContinuation { observers.append($0) }
  }

  func release() {
    open = true
    let currentWaiters = waiters
    waiters.removeAll()
    currentWaiters.forEach { $0.resume() }
  }
}

private nonisolated class EmptyChatClient: ChatCalling, @unchecked Sendable {
  func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { $0.finish() }
  }

  func chats(query _: String, beforeUpdated _: Int64, limit _: Int) async throws -> [ChatSummary] { [] }
  func chatMessages(chatID _: Int64, beforeID _: Int64, limit _: Int) async throws -> [SavedChatMessage] { [] }
  func stopChat(chatID _: Int64) async throws -> Bool { true }
  func renameChat(chatID _: Int64, title _: String) async throws {}
  func deleteChat(chatID _: Int64) async throws {}
}

private nonisolated final class BlockingHistoryClient: EmptyChatClient, @unchecked Sendable {
  private let gate: TestGate

  init(gate: TestGate) { self.gate = gate }

  override func chats(query _: String, beforeUpdated _: Int64, limit _: Int) async throws -> [ChatSummary] {
    await gate.block()
    return [ChatSummary(id: 9, title: "late", createdAt: 1, updatedAt: 2, messages: 1)]
  }
}

private nonisolated final class BlockingMessagesClient: EmptyChatClient, @unchecked Sendable {
  private let gate: TestGate

  init(gate: TestGate) { self.gate = gate }

  override func chatMessages(
    chatID _: Int64,
    beforeID _: Int64,
    limit _: Int
  ) async throws -> [SavedChatMessage] {
    await gate.block()
    return []
  }
}

private actor ChatRequestRecorder {
  private var value: ChatRequest?
  private var waiters: [CheckedContinuation<ChatRequest, Never>] = []
  func set(_ request: ChatRequest) {
    value = request
    waiters.forEach { $0.resume(returning: request) }
    waiters.removeAll()
  }
  func wait() async -> ChatRequest {
    if let value { return value }
    return await withCheckedContinuation { waiters.append($0) }
  }
  func get() -> ChatRequest? { value }
}

private nonisolated final class RequestRecordingChatClient: EmptyChatClient, @unchecked Sendable {
  private let recorder = ChatRequestRecorder()

  override func streamChat(_ request: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { continuation in
      Task {
        await recorder.set(request)
        continuation.yield(.done)
        continuation.finish()
      }
    }
  }

  func requested() async -> ChatRequest? { await recorder.get() }
  func waitForRequest() async -> ChatRequest { await recorder.wait() }
}

private nonisolated final class StoppableChatClient: EmptyChatClient, @unchecked Sendable {
  private let stopGate: TestGate

  init(stopGate: TestGate) { self.stopGate = stopGate }

  override func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { continuation in
      continuation.yield(.chatID(7))
    }
  }

  override func stopChat(chatID _: Int64) async throws -> Bool {
    await stopGate.block()
    return true
  }
}

private actor StoppedChatRecorder {
  private var value: Int64?
  func set(_ chatID: Int64) { value = chatID }
  func get() -> Int64? { value }
}

private nonisolated final class DeferredIDChatClient: EmptyChatClient, @unchecked Sendable {
  private let stopGate: TestGate
  private let recorder = StoppedChatRecorder()
  private let events: AsyncThrowingStream<ChatEvent, Error>
  private let continuation: AsyncThrowingStream<ChatEvent, Error>.Continuation

  init(stopGate: TestGate) {
    self.stopGate = stopGate
    let pair = AsyncThrowingStream<ChatEvent, Error>.makeStream()
    events = pair.stream
    continuation = pair.continuation
  }

  override func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> { events }

  func emit(_ event: ChatEvent) { continuation.yield(event) }

  override func stopChat(chatID: Int64) async throws -> Bool {
    await recorder.set(chatID)
    await stopGate.block()
    return true
  }

  func stoppedChatID() async -> Int64? { await recorder.get() }
}

private nonisolated class ReplacementChatClient: EmptyChatClient, @unchecked Sendable {
  override func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { _ in }
  }

  override func chatMessages(
    chatID _: Int64,
    beforeID _: Int64,
    limit _: Int
  ) async throws -> [SavedChatMessage] {
    [
      SavedChatMessage(
        id: 1,
        role: "user",
        content: "saved question",
        ts: 1,
        reasoning: nil,
        sources: nil,
        state: "complete"
      )
    ]
  }
}

private nonisolated final class AdoptedChatClient: ReplacementChatClient, @unchecked Sendable {
  override func streamChat(_: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    AsyncThrowingStream { continuation in
      continuation.yield(.chatID(73))
    }
  }
}
