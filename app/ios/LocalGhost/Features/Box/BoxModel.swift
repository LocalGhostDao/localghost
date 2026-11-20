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
  private let signedInConnect: SignedInConnect
  private let forgetLeftovers: @MainActor () -> Void
  private let now: () -> Date
  private let pollEvery: Duration

  /// THE SIGNED-IN CHANNEL, one for the session. Home asks the box every five seconds and a chat
  /// being written every second and a half; a channel made for each call (20 November 2026, as
  /// merged) read the Keychain four times on the main actor and shook hands with the box again,
  /// client certificate and all, every time. It is made at the first call after an unlock and
  /// let go when what it was made from changes: the lock, the forgetting, a new unlock, the
  /// device key replaced (the box retires the certificate this one presents), a status the box
  /// refused, the session's end.
  @ObservationIgnored private var signedIn: (channel: any SignedInCalls, session: BoxSession)?
  @ObservationIgnored private var checkingRefusal = false
  @ObservationIgnored private var renewingKey = false

  init(
    credentialStore: any CredentialStoring,
    keyring: any DeviceKeyring,
    sessions: any SessionStoring,
    connect: BoxConnect? = nil,
    signedInConnect: SignedInConnect? = nil,
    forgetLeftovers: @escaping @MainActor () -> Void = { SignedInLeftovers.clear() },
    now: @escaping () -> Date = { Date() },
    pollEvery: Duration = .seconds(1)
  ) {
    self.credentialStore = credentialStore
    self.keyring = keyring
    self.sessions = sessions
    self.connect = connect ?? Self.overTLS(keyring: keyring)
    self.signedInConnect = signedInConnect ?? Self.signedInOverTLS(keyring: keyring)
    self.forgetLeftovers = forgetLeftovers
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

  /// The real signed-in channel: BoxClient over the identity whose key has the tag, with the
  /// session's bearer, telling the model when the box refuses a call.
  static func signedInOverTLS(keyring: any DeviceKeyring) -> SignedInConnect {
    { credentials, tag, token, refused in
      try BoxClient(
        baseURL: credentials.baseURL,
        fingerprint: credentials.certificateFingerprint,
        identity: keyring.identity(tag: tag),
        token: token,
        onRefused: refused
      )
    }
  }

  var canUnlock: Bool { !busy && !pin.isEmpty }

  /// The authenticated channel for the signed-in features, the same one for the whole session.
  /// Feature models keep only this factory, never credentials or bearer material, and every call
  /// is under the same pinned-mTLS rules. With no session, or one past its end, there is none:
  /// this iPhone goes back to the PIN.
  func authenticatedClient() throws -> any SignedInCalls {
    if let signedIn, signedIn.session.isValid(at: now()) {
      return signedIn.channel
    }
    signedIn = nil
    guard let credentials = loadCredentials(), let tag = credentials.keyTag else {
      throw BoxChannelError.appearsDown // loadCredentials has said why
    }
    guard let session = sessions.load(), session.isValid(at: now()) else {
      if screen == .open {
        try? sessions.clear()
        status = nil
        screen = .unlock
        errorMessage = "the session ended: open the box with the PIN"
      }
      throw BoxChannelError.appearsDown
    }
    // called from the channel's own queue when the box answers 502 or 503
    let refused: @Sendable () -> Void = { [weak self] in
      _ = Task { @MainActor [weak self] in
        await self?.signedInCallRefused()
      }
    }
    let channel = try signedInConnect(credentials, tag, session.token, refused)
    signedIn = (channel, session)
    return channel
  }

  /// The box answered a signed-in call as if it were down. One endpoint can do that for its own
  /// reasons, so the status is asked: refused too (locked, the session ended there, the
  /// certificate retired), the session is cleared and this iPhone is back at the PIN; answered,
  /// nothing changes. One check at a time, however many calls were refused together.
  ///
  /// Not while the device key is being replaced: the box retires the old certificate when it
  /// confirms, calls already on their way over the old one come back refused, and the status
  /// asked before the confirmation's answer is kept here would be asked over the old one too.
  /// The refusal is expected and the next call goes over the new key; a box that really locked
  /// is refused again five seconds later.
  func signedInCallRefused() async {
    guard screen == .open, !checkingRefusal, !renewingKey else { return }
    checkingRefusal = true
    defer { checkingRefusal = false }
    // a hand-over whose confirmation was lost: the box has retired the certificate in use, so
    // it is confirmed again first (the box says it already did) and the status goes over the
    // new one
    if (try? credentialStore.load())?.pendingKeyTag != nil {
      await renewKeyIfDue()
    }
    await refresh()
  }

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
    signedIn = nil // a new session is a new channel
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
      signedIn = nil
      try? sessions.clear()
      status = nil
      screen = .unlock
      errorMessage = "the box did not answer: open it with the PIN"
    } catch {
      errorMessage = Self.words(for: error)
    }
  }

  /// Leave the signed-in shell and discard its bearer. Until biometric app locking exists on
  /// iOS, returning to the box PIN is the only lock boundary that does not pretend to secure it.
  func lock() {
    signedIn = nil
    try? sessions.clear()
    pin = ""
    status = nil
    steps = []
    errorMessage = nil
    keyNote = nil
    busy = false
    screen = .unlock
  }

  /// Forget this box: the device key, its certificate and the session leave this iPhone, and so
  /// does what the signed-in screens kept outside the Keychain (Home's cache, the chat last
  /// opened). The box still lists this iPhone until it is retired there.
  func forget() throws {
    signedIn = nil
    try sessions.clear()
    try keyring.removeAll()
    try credentialStore.clear()
    forgetLeftovers()
  }

  // --- the device key ---

  /// The key in use is the QR's, or a day old: make one here and have the box sign for it. A
  /// hand-over the box answered and this iPhone never confirmed is confirmed first.
  func renewKeyIfDue() async {
    guard !renewingKey else { return }
    renewingKey = true
    defer { renewingKey = false }
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
    // the box has retired the certificate the signed-in channel presents: the next call makes
    // one over the new key
    signedIn = nil
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

/// What the signed-in screens ask of the box: the chat's calls and Home's.
typealias SignedInCalls = ChatCalling & HomeServing

/// Makes the signed-in channel: the credentials, the key's tag, the session's bearer, and what to
/// call when the box refuses a call (the tests give their own).
typealias SignedInConnect = @MainActor (
  EnrolmentCredentials, String, String, @escaping @Sendable () -> Void
) throws -> any SignedInCalls

/// What the signed-in screens keep on this iPhone outside the Keychain: the chat last opened (an
/// id in the preferences) and Home's cache (the news, the prices and the last forecast, which
/// names the place it was for). Forgetting the box removes both; until 20 November 2026 they
/// stayed, and the next box enrolled was asked for the last one's chat.
enum SignedInLeftovers {
  static func clear(preferences: UserDefaults = .standard, homeCache: URL = homeCacheFileURL()) {
    ChatModel.forgetLastChat(in: preferences)
    try? FileManager.default.removeItem(at: homeCache)
  }
}
