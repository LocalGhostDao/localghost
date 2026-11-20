import Foundation

nonisolated struct ChatSummary: Decodable, Identifiable, Sendable, Equatable {
  let id: Int64
  let title: String
  let createdAt: Int64
  let updatedAt: Int64
  let messages: Int64
}

nonisolated struct ChatSource: Decodable, Sendable, Equatable, Identifiable {
  let n: Int?
  let title: String
  let url: String
  let kind: String?
  let snippet: String?
  let excerpt: String?
  let source: String?
  let published: String?

  var id: String { "\(n ?? 0):\(url):\(title)" }

  init(
    n: Int? = nil,
    title: String,
    url: String,
    kind: String? = nil,
    snippet: String? = nil,
    excerpt: String? = nil,
    source: String? = nil,
    published: String? = nil
  ) {
    self.n = n
    self.title = title
    self.url = url
    self.kind = kind
    self.snippet = snippet
    self.excerpt = excerpt
    self.source = source
    self.published = published
  }
}

nonisolated struct SavedChatMessage: Decodable, Identifiable, Sendable, Equatable {
  let id: Int64
  let role: String
  let content: String
  let ts: Int64
  let reasoning: String?
  let sources: [ChatSource]?
  let state: String?
}

nonisolated struct ChatHistoryTurn: Encodable, Sendable, Equatable {
  let role: String
  let content: String
}

nonisolated struct ChatRequest: Encodable, Sendable, Equatable {
  let prompt: String
  let think: String
  let incognito: Bool
  let chatId: Int64
  let history: [ChatHistoryTurn]
  let length: String
}

nonisolated enum ChatEvent: Sendable, Equatable {
  case memories([String])
  case chatID(Int64)
  case reasoning(String)
  case token(String)
  case status(String)
  case steps([String])
  case more(queries: [String], why: String)
  case done
}

protocol ChatCalling: Sendable {
  func streamChat(_ request: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error>
  func chats(query: String, beforeUpdated: Int64, limit: Int) async throws -> [ChatSummary]
  func chatMessages(chatID: Int64, beforeID: Int64, limit: Int) async throws -> [SavedChatMessage]
  func stopChat(chatID: Int64) async throws -> Bool
  func renameChat(chatID: Int64, title: String) async throws
  func deleteChat(chatID: Int64) async throws
}

extension BoxClient: ChatCalling {
  func streamChat(_ request: ChatRequest) -> AsyncThrowingStream<ChatEvent, Error> {
    let lines = streamLines("/v1/chat", body: request)
    return AsyncThrowingStream { continuation in
      let task = Task {
        do {
          for try await line in lines {
            for event in ChatSSE.events(from: line) {
              continuation.yield(event)
            }
          }
          continuation.finish()
        } catch is CancellationError {
          continuation.finish()
        } catch {
          continuation.finish(throwing: error)
        }
      }
      continuation.onTermination = { _ in task.cancel() }
    }
  }

  func chats(query: String = "", beforeUpdated: Int64 = 0, limit: Int = 20) async throws -> [ChatSummary] {
    var components = URLComponents()
    components.path = "/v1/chats"
    components.queryItems = [URLQueryItem(name: "limit", value: String(limit))]
    if beforeUpdated > 0 {
      components.queryItems?.append(URLQueryItem(name: "before", value: String(beforeUpdated)))
    }
    if !query.isEmpty {
      components.queryItems?.append(URLQueryItem(name: "q", value: String(query.prefix(120))))
    }
    let answer: ChatListAnswer = try await get(components.string ?? "/v1/chats?limit=20")
    return answer.chats
  }

  func chatMessages(chatID: Int64, beforeID: Int64 = 0, limit: Int = 100) async throws -> [SavedChatMessage] {
    var components = URLComponents()
    components.path = "/v1/chats/messages"
    components.queryItems = [
      URLQueryItem(name: "id", value: String(chatID)),
      URLQueryItem(name: "limit", value: String(limit)),
    ]
    if beforeID > 0 {
      components.queryItems?.append(URLQueryItem(name: "before", value: String(beforeID)))
    }
    let answer: ChatMessagesAnswer = try await get(components.string ?? "/v1/chats/messages")
    return answer.messages
  }

  func stopChat(chatID: Int64) async throws -> Bool {
    let answer: StopChatAnswer = try await post("/v1/chat/stop", body: ChatIDBody(chatId: chatID))
    return answer.stopped
  }

  func renameChat(chatID: Int64, title: String) async throws {
    let answer: ChatOKAnswer = try await post(
      "/v1/chats/rename",
      body: RenameChatBody(id: chatID, title: title)
    )
    guard answer.ok else { throw BoxChannelError.malformed }
  }

  func deleteChat(chatID: Int64) async throws {
    let answer: ChatOKAnswer = try await post("/v1/chats/delete", body: DeleteChatBody(id: chatID))
    guard answer.ok else { throw BoxChannelError.malformed }
  }
}

nonisolated enum ChatSSE {
  static func events(from line: String) -> [ChatEvent] {
    guard line.hasPrefix("data:") else { return [] }
    let raw = line.dropFirst(5).trimmingCharacters(in: .whitespaces)
    guard !raw.isEmpty,
          let data = raw.data(using: .utf8),
          let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
    else {
      return []
    }

    var events: [ChatEvent] = []
    if let context = object["context"] as? [[String: Any]] {
      let memories = context.compactMap { item -> String? in
        guard let snippet = item["snippet"] as? String, !snippet.isEmpty else { return nil }
        let date = item["when"] as? String ?? ""
        let source = item["source"] as? String ?? "archive"
        return ([date, source].filter { !$0.isEmpty }.joined(separator: " , ")) + ": " + snippet
      }
      if !memories.isEmpty { events.append(.memories(memories)) }
    }
    if let note = object["note"] as? String, !note.isEmpty { events.append(.status(note)) }
    if let steps = object["steps"] as? [String], !steps.isEmpty { events.append(.steps(steps)) }
    if let id = integer(object["chatId"]), id > 0 { events.append(.chatID(id)) }
    if let more = object["more"] as? [String: Any],
       let queries = more["queries"] as? [String], !queries.isEmpty
    {
      events.append(.more(queries: queries, why: more["why"] as? String ?? ""))
    }
    if let reasoning = object["r"] as? String { events.append(.reasoning(reasoning)) }
    if let token = object["t"] as? String { events.append(.token(token)) }
    if object["done"] as? Bool == true {
      events.append(.done)
    }
    return events
  }

  private static func integer(_ value: Any?) -> Int64? {
    if let value = value as? Int64 { return value }
    if let value = value as? Int { return Int64(value) }
    if let value = value as? NSNumber { return value.int64Value }
    return nil
  }
}

private nonisolated struct ChatListAnswer: Decodable, Sendable { let chats: [ChatSummary] }
private nonisolated struct ChatMessagesAnswer: Decodable, Sendable { let messages: [SavedChatMessage] }
private nonisolated struct StopChatAnswer: Decodable, Sendable {
  let stopped: Bool
}
private nonisolated struct ChatOKAnswer: Decodable, Sendable { let ok: Bool }
private nonisolated struct ChatIDBody: Encodable, Sendable { let chatId: Int64 }
private nonisolated struct DeleteChatBody: Encodable, Sendable { let id: Int64 }
private nonisolated struct RenameChatBody: Encodable, Sendable {
  let id: Int64
  let title: String
}
