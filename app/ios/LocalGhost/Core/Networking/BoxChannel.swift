import CryptoKit
import Foundation
import Security

// THE CHANNEL TO THE BOX. Mutual TLS with no public trust anywhere:
//
//   - the SERVER is the box when the SHA-256 of the certificate it presents equals the fingerprint
//     the QR carried (the box is its own CA; no other check, the pin is the trust decision, and
//     the host name is not compared, so a box that moves to another address stays the box);
//   - the CLIENT is this phone's identity (its key and the certificate the box signed for it):
//     the box refuses a handshake without one before any PIN is asked.
//
// No redirect is followed, nothing is cached, no cookie is kept. Every refusal the box gives looks
// the same on purpose (a 503 "down" page for a locked box, a missing session, a retired
// certificate), so it is reported as one thing: the box did not answer. The Android app's
// BoxTrust and BoxHttp are the same rules.

enum BoxChannelError: Error, Equatable, LocalizedError {
  case notEnrolled
  case appearsDown
  case unreachable(String)
  case unexpected(Int)
  case malformed

  var errorDescription: String? {
    switch self {
    case .notEnrolled: "This iPhone is not enrolled with a box."
    case .appearsDown: "The box did not answer."
    case .unreachable(let why): "The box could not be reached (\(why))."
    case .unexpected(let code): "The box answered with HTTP \(code)."
    case .malformed: "The box's answer could not be read."
    }
  }
}

/// The pin: the QR's fingerprint as bytes, and the comparison with a presented certificate.
nonisolated enum BoxPin {
  /// "AB:CD:…" (or bare hex) to 32 bytes; nil for anything that is not 64 hex digits.
  static func bytes(fromFingerprint fingerprint: String) -> Data? {
    let hex = fingerprint.filter { $0 != ":" && !$0.isWhitespace }
    guard hex.count == 64, hex.allSatisfy(\.isHexDigit) else { return nil }
    var out = Data(capacity: 32)
    var index = hex.startIndex
    while index < hex.endIndex {
      let next = hex.index(index, offsetBy: 2)
      guard let byte = UInt8(hex[index..<next], radix: 16) else { return nil }
      out.append(byte)
      index = next
    }
    return out
  }

  /// Whether a certificate (DER) is the pinned one, compared in constant time.
  static func matches(certificateDER der: Data, pin: Data) -> Bool {
    let digest = Data(SHA256.hash(data: der))
    guard digest.count == pin.count else { return false }
    var difference: UInt8 = 0
    for (a, b) in zip(digest, pin) {
      difference |= a ^ b
    }
    return difference == 0
  }
}

/// The URLSession delegate that makes the two decisions above.
nonisolated final class BoxTrustDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
  private let pin: Data
  private let clientCredential: URLCredential

  init(pin: Data, identity: SecIdentity) {
    self.pin = pin
    clientCredential = URLCredential(
      identity: identity,
      certificates: nil,
      persistence: .forSession
    )
  }

  /// Test seam for proving that request-specific challenges receive the client credential. The
  /// production path above always constructs this from a Keychain SecIdentity.
  init(pin: Data, clientCredential: URLCredential) {
    self.pin = pin
    self.clientCredential = clientCredential
  }

  func urlSession(
    _ session: URLSession,
    didReceive challenge: URLAuthenticationChallenge
  ) async -> (URLSession.AuthChallengeDisposition, URLCredential?) {
    answer(challenge)
  }

  /// Foundation sends request-specific authentication here and does not fall back to the
  /// session-level callback when this method is absent. Client-certificate selection therefore
  /// has to be handled on the task delegate path as well as server trust above.
  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    didReceive challenge: URLAuthenticationChallenge,
    completionHandler: @escaping @Sendable (
      URLSession.AuthChallengeDisposition,
      URLCredential?
    ) -> Void
  ) {
    let result = answer(challenge)
    completionHandler(result.0, result.1)
  }

  private func answer(
    _ challenge: URLAuthenticationChallenge
  ) -> (URLSession.AuthChallengeDisposition, URLCredential?) {
    switch challenge.protectionSpace.authenticationMethod {
    case NSURLAuthenticationMethodServerTrust:
      guard let trust = challenge.protectionSpace.serverTrust,
            let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
            let leaf = chain.first,
            BoxPin.matches(certificateDER: SecCertificateCopyData(leaf) as Data, pin: pin)
      else {
        return (.cancelAuthenticationChallenge, nil)
      }
      return (.useCredential, URLCredential(trust: trust))
    case NSURLAuthenticationMethodClientCertificate:
      return (.useCredential, clientCredential)
    default:
      return (.cancelAuthenticationChallenge, nil)
    }
  }

  func urlSession(
    _ session: URLSession,
    task: URLSessionTask,
    willPerformHTTPRedirection response: HTTPURLResponse,
    newRequest request: URLRequest,
    completionHandler: @escaping @Sendable (URLRequest?) -> Void
  ) {
    // The box never redirects; an answer that says "go elsewhere" is not followed.
    completionHandler(nil)
  }
}

// --- what the box answers (secd: unlock.go, status_http.go, rekey.go) ---

nonisolated struct UnlockStarted: Decodable, Sendable {
  let started: Bool?
  let run: String?
}

nonisolated struct UnlockPoll: Decodable, Sendable {
  nonisolated struct Stage: Decodable, Sendable, Equatable {
    let stage: String
    let state: String
  }

  let stages: [Stage]
  let done: Bool
  let failed: String?
  let token: String?
  let expiresAt: String?
}

nonisolated struct BoxStatus: Decodable, Sendable, Equatable {
  nonisolated struct Service: Decodable, Sendable, Equatable {
    let name: String
    let critical: Bool?
    let state: String
    let restarts: Int?
    let lastErr: String?
    let detail: String?
  }

  nonisolated struct Datastore: Decodable, Sendable, Equatable {
    let name: String
    let state: String
    let detail: String?
  }

  let services: [Service]
  let datastores: [Datastore]?
}

nonisolated struct RekeyAnswer: Decodable, Sendable {
  let ok: Bool
  let cert: String
  let lifeDays: Int?
  let renewAfterHours: Int?
}

private nonisolated struct OkAnswer: Decodable, Sendable {
  let ok: Bool
  let already: Bool?
}

/// The calls the app makes to the box (BoxClient over TLS; a fake in the tests).
protocol BoxCalls: Sendable {
  func unlockStart(pin: String) async throws -> String
  func unlockPoll(run: String) async throws -> UnlockPoll
  func status() async throws -> BoxStatus
  func rekey(spki: Data, signature: Data) async throws -> RekeyAnswer
  func rekeyConfirm() async throws
}

/// One connection's worth of calls to the box, over one identity. Immutable; made again when the
/// identity or the session changes.
nonisolated final class BoxClient: BoxCalls, Sendable {
  private let base: URL
  private let session: URLSession
  private let token: String?
  /// Told when the box answers a call as if it were down (502, 503): locked, the session ended,
  /// or the certificate retired. The signed-in channel gives one, so the app can go back to the
  /// PIN; the unlock's own client gives none.
  private let refused: (@Sendable () -> Void)?

  init(
    baseURL: String,
    fingerprint: String,
    identity: SecIdentity,
    token: String?,
    onRefused: (@Sendable () -> Void)? = nil
  ) throws {
    guard let base = URL(string: baseURL), base.scheme?.lowercased() == "https", base.host != nil else {
      throw BoxChannelError.notEnrolled
    }
    guard let pin = BoxPin.bytes(fromFingerprint: fingerprint) else {
      throw BoxChannelError.notEnrolled
    }
    let configuration = URLSessionConfiguration.ephemeral
    configuration.timeoutIntervalForRequest = 30
    // A model answer can legitimately run for several minutes. The per-request timeout still
    // bounds ordinary calls; this resource ceiling must not terminate a healthy chat stream.
    configuration.timeoutIntervalForResource = 15 * 60
    configuration.waitsForConnectivity = false
    configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
    configuration.urlCache = nil
    configuration.httpCookieStorage = nil
    configuration.httpShouldSetCookies = false
    configuration.tlsMinimumSupportedProtocolVersion = .TLSv12
    self.base = base
    self.token = token
    refused = onRefused
    session = URLSession(
      configuration: configuration,
      delegate: BoxTrustDelegate(pin: pin, identity: identity),
      delegateQueue: nil
    )
  }

  deinit {
    session.finishTasksAndInvalidate() // the session holds its delegate until it is let go
  }

  /// POST /v1/unlock {"pin"}: the box starts opening and names the run.
  func unlockStart(pin: String) async throws -> String {
    let answer: UnlockStarted = try await post("/v1/unlock", body: ["pin": pin])
    guard let run = answer.run, !run.isEmpty else { throw BoxChannelError.malformed }
    return run
  }

  /// GET /v1/unlock/poll?run=: the steps so far; the session token rides on the poll that says done.
  func unlockPoll(run: String) async throws -> UnlockPoll {
    var components = URLComponents()
    components.path = "/v1/unlock/poll"
    components.queryItems = [URLQueryItem(name: "run", value: run)]
    return try await get(components.string ?? "/v1/unlock/poll")
  }

  /// GET /v1/status: the daemons, the datastores. Needs the session.
  func status() async throws -> BoxStatus {
    try await get("/v1/status")
  }

  /// POST /v1/device/rekey: a certificate for a key this phone made, over the current identity.
  func rekey(spki: Data, signature: Data) async throws -> RekeyAnswer {
    let answer: RekeyAnswer = try await post(
      "/v1/device/rekey",
      body: ["spki": spki.base64EncodedString(), "sig": signature.base64EncodedString()]
    )
    guard answer.ok, !answer.cert.isEmpty else { throw BoxChannelError.malformed }
    return answer
  }

  /// POST /v1/device/rekey/confirm, over the NEW identity: the box retires the old certificate.
  func rekeyConfirm() async throws {
    let answer: OkAnswer = try await post("/v1/device/rekey/confirm", body: [String: String]())
    guard answer.ok else { throw BoxChannelError.malformed }
  }

  func get<Answer: Decodable & Sendable>(_ path: String, timeout: TimeInterval? = nil) async throws -> Answer {
    let request = try makeRequest("GET", path, body: nil, timeout: timeout)
    return try await answer(for: request)
  }

  func post<Body: Encodable & Sendable, Answer: Decodable & Sendable>(
    _ path: String,
    body: Body,
    timeout: TimeInterval? = nil
  ) async throws -> Answer {
    let request = try makeRequest("POST", path, body: try JSONEncoder().encode(body), timeout: timeout)
    return try await answer(for: request)
  }

  /// Complete lines from a streamed response. URLSession owns byte framing; callers own the
  /// protocol carried by each line (chat uses SSE `data:` records). Cancelling iteration cancels
  /// the request rather than leaving a model generation detached from its screen.
  func streamLines<Body: Encodable & Sendable>(
    _ path: String,
    body: Body,
    timeout: TimeInterval = 15 * 60
  ) -> AsyncThrowingStream<String, Error> {
    let request: URLRequest
    do {
      request = try makeRequest("POST", path, body: try JSONEncoder().encode(body), timeout: timeout)
    } catch {
      return AsyncThrowingStream { $0.finish(throwing: error) }
    }

    return AsyncThrowingStream { continuation in
      let task = Task { [session, refused] in
        do {
          let (bytes, response) = try await session.bytes(for: request)
          try Self.validate(response, onRefused: refused)
          for try await line in bytes.lines {
            try Task.checkCancellation()
            continuation.yield(line)
          }
          continuation.finish()
        } catch is CancellationError {
          continuation.finish()
        } catch let error as URLError {
          if error.code == .cancelled, Task.isCancelled {
            continuation.finish()
          } else {
            continuation.finish(throwing: BoxChannelError.unreachable(Self.describe(error)))
          }
        } catch {
          continuation.finish(throwing: error)
        }
      }
      continuation.onTermination = { _ in task.cancel() }
    }
  }

  private func makeRequest(
    _ method: String,
    _ path: String,
    body: Data?,
    timeout: TimeInterval?
  ) throws -> URLRequest {
    guard let url = Self.address(path, on: base) else { throw BoxChannelError.malformed }
    var request = URLRequest(url: url)
    request.httpMethod = method
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    if let timeout { request.timeoutInterval = timeout }
    if let token {
      request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }
    if let body {
      request.setValue("application/json", forHTTPHeaderField: "Content-Type")
      request.httpBody = body
    }
    return request
  }

  private func answer<Answer: Decodable & Sendable>(for request: URLRequest) async throws -> Answer {
    let data: Data
    let response: URLResponse
    do {
      (data, response) = try await session.data(for: request)
    } catch let error as URLError {
      if error.code == .cancelled, Task.isCancelled { throw CancellationError() }
      #if DEBUG
        NSLog(
          "[LocalGhost Network] request failed: code=%ld detail=%@",
          error.errorCode,
          error.localizedDescription
        )
      #endif
      throw BoxChannelError.unreachable(Self.describe(error))
    }
    try Self.validate(response, onRefused: refused)
    do {
      return try JSONDecoder().decode(Answer.self, from: data)
    } catch {
      throw BoxChannelError.malformed
    }
  }

  private static func validate(_ response: URLResponse, onRefused: (@Sendable () -> Void)?) throws {
    guard let http = response as? HTTPURLResponse else { throw BoxChannelError.malformed }
    switch http.statusCode {
    case 200..<300: return
    case 502, 503:
      onRefused?()
      throw BoxChannelError.appearsDown
    default: throw BoxChannelError.unexpected(http.statusCode)
    }
  }

  /// Where a call goes: the path on the box's own origin, or nil. The calls are general now
  /// (get, post, streamLines), and a path that is a whole address ("https://elsewhere/",
  /// "//elsewhere/") would otherwise be asked for with the session's bearer. The pin already
  /// refuses another host's certificate; this refuses the request before it is made.
  static func address(_ path: String, on base: URL) -> URL? {
    guard let url = URL(string: path, relativeTo: base)?.absoluteURL,
          url.scheme?.lowercased() == "https",
          url.host == base.host,
          url.port == base.port
    else { return nil }
    return url
  }

  private static func describe(_ error: URLError) -> String {
    switch error.code {
    case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot,
         .serverCertificateNotYetValid, .cancelled:
      // the pin refused it (a cancelled challenge surfaces as .cancelled)
      "the box's certificate is not the one in the QR"
    case .clientCertificateRejected, .clientCertificateRequired:
      "the box refused this iPhone's certificate"
    case .timedOut:
      "no answer in time"
    case .cannotFindHost, .dnsLookupFailed:
      "the address does not resolve"
    case .cannotConnectToHost, .networkConnectionLost, .notConnectedToInternet:
      "no route to it from here"
    default:
      error.localizedDescription
    }
  }
}
