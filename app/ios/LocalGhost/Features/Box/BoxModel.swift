import Foundation
import Observation

// AFTER ENROLMENT: open the box with the PIN, keep the session it hands over, replace the QR's key
// with one made here, and show what the box is running. The order and the words follow the Android
// app (BoxClient.submitPinStreaming, DeviceCert): one poll a second while the box opens, the
// session taken from the poll that says done, the rekey after an unlock and then once a day.

/// One step of the box opening, as the screen shows it.
struct UnlockStep: Equatable, Identifiable {
  let stage: String
  let state: String

  var id: String { stage }

  /// What the step does, in the app's words.
  var words: String {
    switch stage {
    case "RESOLVE": "checking the PIN"
    case "UNSEAL": "unsealing the vault key"
    case "MOUNT": "opening the vault"
    case "START_DB": "starting the database"
    case "START_CACHE": "starting the cache"
    case "DAEMONS": "starting the daemons"
    case "MODEL": "loading the model"
    case "READY": "ready"
    default: stage.lowercased().replacingOccurrences(of: "_", with: " ")
    }
  }

  var mark: String {
    switch state {
    case "COMPLETE", "SKIPPED": "done"
    case "RUNNING": "..."
    case "ERRORED": "failed"
    default: ""
    }
  }
}

@MainActor
@Observable
final class BoxModel {
  enum Screen: Equatable {
    case unlock
    case opening
    case open
  }

  private(set) var screen: Screen = .unlock
  private(set) var steps: [UnlockStep] = []
  private(set) var status: BoxStatus?
  private(set) var errorMessage: String?
  /// A line about the device key: replaced, or why not yet.
  private(set) var keyNote: String?
  private(set) var busy = false
  var pin = ""

  private let credentialStore: any CredentialStoring
  private let keyring: any DeviceKeyring
  private let sessions: any SessionStoring
  private let connect: BoxConnect
  private let now: () -> Date
  private let pollEvery: Duration

  init(
    credentialStore: any CredentialStoring,
    keyring: any DeviceKeyring,
    sessions: any SessionStoring,
    connect: BoxConnect? = nil,
    now: @escaping () -> Date = { Date() },
    pollEvery: Duration = .seconds(1)
  ) {
    self.credentialStore = credentialStore
    self.keyring = keyring
    self.sessions = sessions
    self.connect = connect ?? Self.overTLS(keyring: keyring)
    self.now = now
    self.pollEvery = pollEvery
  }

  /// The real connection: BoxClient over the identity whose key has the tag.
  static func overTLS(keyring: any DeviceKeyring) -> BoxConnect {
    { credentials, tag, token in
      try BoxClient(
        baseURL: credentials.baseURL,
        fingerprint: credentials.certificateFingerprint,
        identity: keyring.identity(tag: tag),
        token: token
      )
    }
  }

  var canUnlock: Bool { !busy && !pin.isEmpty }

  /// At launch: a session still good goes straight to the status; otherwise the PIN.
  func start() async {
    guard let session = sessions.load(), session.isValid(at: now()) else {
      screen = .unlock
      return
    }
    screen = .open
    // the key first: a hand-over left unconfirmed would have the status asked over a retired
    // certificate
    await renewKeyIfDue()
    await refresh()
  }

  /// The PIN to the box, then a poll a second until it says done or failed.
  func unlock() async {
    guard canUnlock, let credentials = loadCredentials() else { return }
    let pin = self.pin
    self.pin = "" // not kept a moment longer than the request needs it
    busy = true
    errorMessage = nil
    steps = []
    screen = .opening
    defer { busy = false }

    do {
      var box = try client(for: credentials, tag: credentials.keyTag, token: nil)
      let run: String
      do {
        run = try await box.unlockStart(pin: pin)
      } catch BoxChannelError.appearsDown where credentials.pendingKeyTag != nil {
        // a rekey the box confirmed whose answer never arrived: the old certificate is retired
        // and the box answers it as if it were down, so the new one opens it (and is adopted
        // when the hand-over is confirmed again below, which the box says it already was)
        box = try client(for: credentials, tag: credentials.pendingKeyTag, token: nil)
        run = try await box.unlockStart(pin: pin)
      }
      while true {
        let poll = try await box.unlockPoll(run: run)
        steps = poll.stages.map { UnlockStep(stage: $0.stage, state: $0.state) }
        if let failed = poll.failed, !failed.isEmpty {
          throw UnlockFailure.failed(failed)
        }
        if poll.done {
          guard let token = poll.token, !token.isEmpty,
                let expiry = poll.expiresAt.flatMap(Self.parseExpiry)
          else {
            throw UnlockFailure.noSession
          }
          try sessions.save(BoxSession(token: token, expiresAt: expiry))
          break
        }
        try await Task.sleep(for: pollEvery)
      }
    } catch {
      screen = .unlock
      errorMessage = Self.words(for: error)
      return
    }

    screen = .open
    // the key first: a hand-over left unconfirmed would have the status asked over a retired
    // certificate
    await renewKeyIfDue()
    await refresh()
  }

  /// The status again.
  func refresh() async {
    guard let credentials = loadCredentials(), let session = sessions.load(), session.isValid(at: now()) else {
      screen = .unlock
      return
    }
    do {
      status = try await client(for: credentials, tag: credentials.keyTag, token: session.token).status()
      errorMessage = nil
    } catch BoxChannelError.appearsDown {
      // a locked box, a session that ended, a certificate retired: the PIN again
      try? sessions.clear()
      status = nil
      screen = .unlock
      errorMessage = "the box did not answer: open it with the PIN"
    } catch {
      errorMessage = Self.words(for: error)
    }
  }

  /// Forget this box: the device key, its certificate and the session leave this iPhone. The box
  /// still lists this iPhone until it is retired there.
  func forget() throws {
    try sessions.clear()
    try keyring.removeAll()
    try credentialStore.clear()
  }

  // --- the device key ---

  /// The key in use is the QR's, or a day old: make one here and have the box sign for it. A
  /// hand-over the box answered and this iPhone never confirmed is confirmed first.
  func renewKeyIfDue() async {
    guard var credentials = loadCredentials(), let session = sessions.load(), session.isValid(at: now()) else { return }
    do {
      if credentials.pendingKeyTag != nil {
        try await confirmPending(&credentials, token: session.token)
      }
      if let made = credentials.rekeyedAt,
         now().timeIntervalSince(made) < Double(credentials.renewAfterHours ?? 24) * 3600 {
        return
      }
      try await rekey(&credentials, token: session.token)
      keyNote = "this iPhone now uses a key it made itself, which never leaves it; the QR's key is retired"
    } catch {
      keyNote = credentials.rekeyedAt == nil
        ? "the key from the QR is still in use; it is replaced at the next unlock (\(Self.words(for: error)))"
        : "the key was not renewed today; it is tried again at the next unlock (\(Self.words(for: error)))"
    }
  }

  private func rekey(_ credentials: inout EnrolmentCredentials, token: String) async throws {
    guard let presented = PEM.der(from: credentials.deviceCertificatePEM, type: "CERTIFICATE") else {
      throw DeviceKeyError.badCertificate
    }
    let key = try keyring.makeKey()
    let answer: RekeyAnswer
    do {
      let signature = try keyring.sign(RekeyProof.message(presentedCertificateDER: presented, spki: key.spki), tag: key.tag)
      answer = try await client(for: credentials, tag: credentials.keyTag, token: token).rekey(spki: key.spki, signature: signature)
      try keyring.installCertificate(pem: answer.cert, tag: key.tag)
    } catch {
      keyring.remove(tag: key.tag) // the box signed nothing for it, or it cannot be used
      throw error
    }
    // kept before the confirmation: if its answer is lost, the next launch confirms again and the
    // box says it already did (the old certificate is retired by then, so it must not be the one
    // this iPhone falls back to)
    credentials.pendingKeyTag = key.tag
    credentials.pendingCertificatePEM = answer.cert
    credentials.renewAfterHours = answer.renewAfterHours
    try credentialStore.save(credentials)
    try await confirmPending(&credentials, token: token)
  }

  private func confirmPending(_ credentials: inout EnrolmentCredentials, token: String) async throws {
    guard let tag = credentials.pendingKeyTag, let certificate = credentials.pendingCertificatePEM else { return }
    try await client(for: credentials, tag: tag, token: token).rekeyConfirm()
    let old = credentials.keyTag
    credentials.deviceCertificatePEM = certificate
    credentials.keyTag = tag
    credentials.rekeyedAt = now()
    credentials.pendingKeyTag = nil
    credentials.pendingCertificatePEM = nil
    try credentialStore.save(credentials)
    if let old, old != tag {
      keyring.remove(tag: old)
    }
  }

  // --- helpers ---

  private func loadCredentials() -> EnrolmentCredentials? {
    guard let credentials = try? credentialStore.load(), credentials.keyTag != nil else {
      errorMessage = "this iPhone's enrolment could not be read"
      return nil
    }
    return credentials
  }

  private func client(for credentials: EnrolmentCredentials, tag: String?, token: String?) throws -> any BoxCalls {
    guard let tag else { throw BoxChannelError.notEnrolled }
    return try connect(credentials, tag, token)
  }

  nonisolated static func parseExpiry(_ value: String) -> Date? {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = formatter.date(from: value) { return date }
    formatter.formatOptions = [.withInternetDateTime]
    return formatter.date(from: value)
  }

  private static func words(for error: Error) -> String {
    switch error {
    case UnlockFailure.failed(let why):
      "the box did not open: \(why)"
    case UnlockFailure.noSession:
      "the box opened but handed over no session; open it again"
    case BoxChannelError.appearsDown:
      "the box did not answer"
    default:
      error.localizedDescription
    }
  }
}

enum UnlockFailure: Error, Equatable {
  case failed(String)
  case noSession
}

/// Makes the calls to the box over the identity whose key has the tag (the tests give their own).
typealias BoxConnect = @MainActor (EnrolmentCredentials, String, String?) throws -> any BoxCalls
