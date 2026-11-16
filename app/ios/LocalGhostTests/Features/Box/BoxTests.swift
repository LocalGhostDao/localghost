import CryptoKit
import Foundation
import Security
import Testing

@testable import LocalGhost

// The channel's rules and the box model's order, against fakes: no network, no Secure Enclave.
// The vectors (the SPKI prefix, the rekey message) were checked against the box's own Go
// (crypto/x509 MarshalPKIXPublicKey, secd's rekeyMessageV2).

struct BoxPinTests {
  // SHA-256("abc")
  private let abc = "BA:78:16:BF:8F:01:CF:EA:41:41:40:DE:5D:AE:22:23:B0:03:61:A3:96:17:7A:9C:B4:10:FF:61:F2:00:15:AD"

  @Test
  func theFingerprintIsThePin() throws {
    let pin = try #require(BoxPin.bytes(fromFingerprint: abc))
    #expect(pin.count == 32)
    #expect(BoxPin.matches(certificateDER: Data("abc".utf8), pin: pin))
    #expect(!BoxPin.matches(certificateDER: Data("abd".utf8), pin: pin))
    #expect(!BoxPin.matches(certificateDER: Data("abc".utf8), pin: pin.dropLast()))
    // bare hex, as the QR carries it, is the same pin
    #expect(BoxPin.bytes(fromFingerprint: abc.replacingOccurrences(of: ":", with: "").lowercased()) == pin)
  }

  @Test
  func anythingElseIsNoPin() {
    #expect(BoxPin.bytes(fromFingerprint: "") == nil)
    #expect(BoxPin.bytes(fromFingerprint: String(repeating: "AB", count: 31)) == nil)
    #expect(BoxPin.bytes(fromFingerprint: String(repeating: "ZZ", count: 32)) == nil)
  }
}

struct BoxTrustDelegateTests {
  @Test
  func taskLevelClientCertificateChallengeUsesTheDeviceCredential() throws {
    let credential = URLCredential(user: "device", password: "test", persistence: .forSession)
    let delegate = BoxTrustDelegate(pin: Data(repeating: 0, count: 32), clientCredential: credential)
    let protectionSpace = URLProtectionSpace(
      host: "box.local",
      port: 8443,
      protocol: "https",
      realm: nil,
      authenticationMethod: NSURLAuthenticationMethodClientCertificate
    )
    let challenge = URLAuthenticationChallenge(
      protectionSpace: protectionSpace,
      proposedCredential: nil,
      previousFailureCount: 0,
      failureResponse: nil,
      error: nil,
      sender: ChallengeSender()
    )
    let task = URLSession.shared.dataTask(with: try #require(URL(string: "https://box.local:8443/")))
    defer { task.cancel() }

    let capture = ChallengeCapture()
    delegate.urlSession(URLSession.shared, task: task, didReceive: challenge) {
      capture.disposition = $0
      capture.credential = $1
    }

    #expect(capture.disposition == .useCredential)
    #expect(capture.credential === credential)
  }
}

struct RekeyProofTests {
  @Test
  func theSPKIIsP256() throws {
    let point = Data([0x04]) + Data(repeating: 0x11, count: 64)
    let spki = try #require(RekeyProof.spki(x963PublicKey: point))
    #expect(spki.count == 91)
    #expect(Array(spki.prefix(26)) == RekeyProof.p256SPKIPrefix)
    #expect(spki.suffix(65) == point)
    #expect(RekeyProof.spki(x963PublicKey: Data([0x02]) + Data(repeating: 0x11, count: 64)) == nil)
    #expect(RekeyProof.spki(x963PublicKey: Data(repeating: 0x04, count: 33)) == nil)
  }

  @Test
  func theMessageIsTheBoxs() {
    let message = RekeyProof.message(presentedCertificateDER: Data("abc".utf8), spki: Data([1, 2, 3]))
    let want = Data("localghost rekey v2\nba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\n".utf8) + Data([1, 2, 3])
    #expect(message == want)
  }

  @Test
  func pemToDER() {
    let der = Data([0x30, 0x82, 0x01, 0x0A, 0xFF])
    let pem = "-----BEGIN CERTIFICATE-----\n\(der.base64EncodedString())\n-----END CERTIFICATE-----\n"
    #expect(PEM.der(from: pem, type: "CERTIFICATE") == der)
    #expect(PEM.der(from: pem, type: "PRIVATE KEY") == nil)
    #expect(PEM.der(from: "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----", type: "CERTIFICATE") == nil)
  }
}

struct BoxAnswerTests {
  @Test
  func thePollAsSecdWritesIt() throws {
    let json = """
    {"stages":[{"stage":"RESOLVE","state":"COMPLETE"},{"stage":"MOUNT","state":"RUNNING"}],
     "done":true,"token":"tok","expiresAt":"2026-11-18T00:30:10Z","ttlSeconds":172800,
     "model":{"phase":"loading","percent":40}}
    """
    let poll = try JSONDecoder().decode(UnlockPoll.self, from: Data(json.utf8))
    #expect(poll.stages.count == 2)
    #expect(poll.stages[1] == UnlockPoll.Stage(stage: "MOUNT", state: "RUNNING"))
    #expect(poll.done)
    #expect(poll.token == "tok")
    #expect(poll.failed == nil)
    #expect(BoxModel.parseExpiry(try #require(poll.expiresAt)) == Date(timeIntervalSince1970: 1_794_961_810))
    let fractional = try #require(BoxModel.parseExpiry("2026-11-18T00:30:10.250Z"))
    #expect(abs(fractional.timeIntervalSince1970 - 1_794_961_810.25) < 0.001)
    #expect(BoxModel.parseExpiry("tomorrow") == nil)
  }

  @Test
  func theStatusAsSecdWritesIt() throws {
    let json = """
    {"services":[{"name":"ghost.framed","critical":true,"state":"running","restarts":0,"code":0,"detail":"backlog: 12 frames"}],
     "datastores":[{"name":"postgres","state":"up","detail":""}],
     "volume":{"usedGB":1.5},"host":{"cores":16}}
    """
    let status = try JSONDecoder().decode(BoxStatus.self, from: Data(json.utf8))
    #expect(status.services.first?.name == "ghost.framed")
    #expect(status.services.first?.detail == "backlog: 12 frames")
    #expect(status.datastores?.first?.state == "up")
  }

  @Test
  func aSessionEndsAMinuteEarly() {
    let session = BoxSession(token: "t", expiresAt: Date(timeIntervalSince1970: 1000))
    #expect(session.isValid(at: Date(timeIntervalSince1970: 900)))
    #expect(!session.isValid(at: Date(timeIntervalSince1970: 950)))
  }
}

@MainActor
struct InstallMarkTests {
  @Test
  func theFirstLaunchClearsWhatAnEarlierInstallLeft() throws {
    let defaults = try #require(UserDefaults(suiteName: "ai.localghost.tests.\(UUID().uuidString)"))
    let credentials = MemoryCredentialStore(EnrolmentFixture.credentials(tag: "left"))
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(BoxSession(token: "old", expiresAt: .distantFuture))

    #expect(InstallMark.clearLeftovers(defaults: defaults, credentials: credentials, keyring: keyring, sessions: sessions))
    #expect(try credentials.load() == nil)
    #expect(sessions.load() == nil)
    #expect(keyring.removedAll == 1)

    // the next launch leaves an enrolment made since alone
    try credentials.save(EnrolmentFixture.credentials(tag: "new"))
    #expect(!InstallMark.clearLeftovers(defaults: defaults, credentials: credentials, keyring: keyring, sessions: sessions))
    #expect(try credentials.load()?.keyTag == "new")
    #expect(keyring.removedAll == 1)
  }

  @Test
  func aClearingThatFailedIsTriedAgain() throws {
    let defaults = try #require(UserDefaults(suiteName: "ai.localghost.tests.\(UUID().uuidString)"))
    let keyring = RecordingKeyring()
    keyring.refuseRemoveAll = true
    let credentials = MemoryCredentialStore(nil)
    let sessions = MemorySessionStore(nil)
    #expect(!InstallMark.clearLeftovers(defaults: defaults, credentials: credentials, keyring: keyring, sessions: sessions))
    #expect(!defaults.bool(forKey: InstallMark.key))
    keyring.refuseRemoveAll = false
    #expect(InstallMark.clearLeftovers(defaults: defaults, credentials: credentials, keyring: keyring, sessions: sessions))
  }
}

@MainActor
struct BoxModelTests {
  private let now = Date(timeIntervalSince1970: 1_794_800_000)

  private func makeModel(
    _ credentials: MemoryCredentialStore,
    _ keyring: RecordingKeyring,
    _ sessions: MemorySessionStore,
    _ box: FakeBox
  ) -> BoxModel {
    let now = self.now
    return BoxModel(
      credentialStore: credentials,
      keyring: keyring,
      sessions: sessions,
      connect: { _, tag, token in box.over(tag: tag, token: token) },
      now: { now },
      pollEvery: .zero
    )
  }

  @Test
  func thePINOpensTheBoxAndTheQRsKeyIsReplaced() async throws {
    let credentials = MemoryCredentialStore(EnrolmentFixture.credentials(tag: "qr"))
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(nil)
    let box = FakeBox()
    box.polls = [
      UnlockPoll(stages: [.init(stage: "RESOLVE", state: "RUNNING")], done: false, failed: nil, token: nil, expiresAt: nil),
      UnlockPoll(stages: [.init(stage: "RESOLVE", state: "COMPLETE")], done: true, failed: nil, token: "tok", expiresAt: "2026-11-18T00:30:10Z"),
    ]
    let model = makeModel(credentials, keyring, sessions, box)
    model.pin = "123456"

    await model.unlock()

    #expect(model.screen == .open)
    #expect(model.pin.isEmpty)
    #expect(box.pins == ["123456"])
    #expect(sessions.load() == BoxSession(token: "tok", expiresAt: Date(timeIntervalSince1970: 1_794_961_810)))
    // the rekey: over the QR's identity, the proof bound to the QR's certificate, then the
    // confirmation over the new one, then the status over the new one
    #expect(box.calls == ["unlock@qr", "poll@qr", "poll@qr", "rekey@qr", "confirm@made-1", "status@made-1"])
    let qrDER = try #require(PEM.der(from: EnrolmentFixture.qrCertificate, type: "CERTIFICATE"))
    #expect(keyring.signed.first == RekeyProof.message(presentedCertificateDER: qrDER, spki: RecordingKeyring.spki))
    #expect(box.rekeyed.first?.spki == RecordingKeyring.spki)
    let after = try #require(try credentials.load())
    #expect(after.keyTag == "made-1")
    #expect(after.deviceCertificatePEM == FakeBox.newCertificate)
    #expect(after.rekeyedAt == now)
    #expect(after.pendingKeyTag == nil)
    #expect(keyring.removed == ["qr"])
    #expect(model.status != nil)
  }

  @Test
  func aRefusedPINLeavesNoSession() async throws {
    let credentials = MemoryCredentialStore(EnrolmentFixture.credentials(tag: "qr"))
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(nil)
    let box = FakeBox()
    box.polls = [UnlockPoll(stages: [.init(stage: "RESOLVE", state: "ERRORED")], done: false, failed: "unlock failed", token: nil, expiresAt: nil)]
    let model = makeModel(credentials, keyring, sessions, box)
    model.pin = "000000"

    await model.unlock()

    #expect(model.screen == .unlock)
    #expect(model.pin.isEmpty)
    #expect(model.errorMessage?.contains("unlock failed") == true)
    #expect(sessions.load() == nil)
    #expect(box.calls == ["unlock@qr", "poll@qr"])
    #expect(try credentials.load()?.keyTag == "qr")
  }

  @Test
  func aLostConfirmationIsConfirmedNextTime() async throws {
    let credentials = MemoryCredentialStore(EnrolmentFixture.credentials(tag: "qr"))
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(BoxSession(token: "tok", expiresAt: .distantFuture))
    let box = FakeBox()
    box.confirmFails = 1
    let model = makeModel(credentials, keyring, sessions, box)

    await model.renewKeyIfDue()
    var kept = try #require(try credentials.load())
    #expect(kept.keyTag == "qr")
    #expect(kept.pendingKeyTag == "made-1")
    #expect(kept.pendingCertificatePEM == FakeBox.newCertificate)
    #expect(keyring.removed.isEmpty) // the QR's key stays until the box has confirmed

    await model.renewKeyIfDue()
    kept = try #require(try credentials.load())
    #expect(kept.keyTag == "made-1")
    #expect(kept.pendingKeyTag == nil)
    #expect(keyring.removed == ["qr"])
    #expect(box.calls.filter { $0.hasPrefix("rekey") }.count == 1) // confirmed, not asked again
  }

  @Test
  func aRetiredCertificateOpensWithTheNewOne() async throws {
    var stored = EnrolmentFixture.credentials(tag: "old")
    stored.pendingKeyTag = "new"
    stored.pendingCertificatePEM = FakeBox.newCertificate
    let credentials = MemoryCredentialStore(stored)
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(nil)
    let box = FakeBox()
    box.downFor = ["old"]
    box.polls = [UnlockPoll(stages: [], done: true, failed: nil, token: "tok", expiresAt: "2026-11-18T00:30:10Z")]
    let model = makeModel(credentials, keyring, sessions, box)
    model.pin = "123456"

    await model.unlock()

    #expect(model.screen == .open)
    #expect(box.calls.prefix(4) == ["unlock@old", "unlock@new", "poll@new", "confirm@new"])
    let after = try #require(try credentials.load())
    #expect(after.keyTag == "new")
    #expect(keyring.removed.contains("old"))
  }

  @Test
  func aKeyMadeTodayIsNotRenewed() async throws {
    var stored = EnrolmentFixture.credentials(tag: "made-0")
    stored.rekeyedAt = now.addingTimeInterval(-3600)
    let credentials = MemoryCredentialStore(stored)
    let box = FakeBox()
    let model = makeModel(credentials, RecordingKeyring(), MemorySessionStore(BoxSession(token: "tok", expiresAt: .distantFuture)), box)

    await model.renewKeyIfDue()

    #expect(box.calls.isEmpty)
  }

  @Test
  func forgettingRemovesEverything() throws {
    let credentials = MemoryCredentialStore(EnrolmentFixture.credentials(tag: "qr"))
    let keyring = RecordingKeyring()
    let sessions = MemorySessionStore(BoxSession(token: "tok", expiresAt: .distantFuture))
    let model = makeModel(credentials, keyring, sessions, FakeBox())

    try model.forget()

    #expect(try credentials.load() == nil)
    #expect(sessions.load() == nil)
    #expect(keyring.removedAll == 1)
  }
}

// --- fakes ---

enum EnrolmentFixture {
  static let qrCertificate = "-----BEGIN CERTIFICATE-----\n\(Data([0x30, 0x03, 0x02, 0x01, 0x01]).base64EncodedString())\n-----END CERTIFICATE-----\n"

  static func credentials(tag: String) -> EnrolmentCredentials {
    EnrolmentCredentials(
      baseURL: "https://box.local:8443",
      deviceName: "Test iPhone",
      certificateFingerprint: String(repeating: "AB:", count: 31) + "AB",
      deviceCertificatePEM: qrCertificate,
      keyTag: tag
    )
  }
}

final class MemoryCredentialStore: CredentialStoring, @unchecked Sendable {
  private let lock = NSLock()
  private var stored: EnrolmentCredentials?

  init(_ stored: EnrolmentCredentials?) { self.stored = stored }

  func load() throws -> EnrolmentCredentials? { lock.withLock { stored } }
  func save(_ credentials: EnrolmentCredentials) throws { lock.withLock { stored = credentials } }
  func clear() throws { lock.withLock { stored = nil } }
}

private final class ChallengeSender: NSObject, URLAuthenticationChallengeSender {
  func use(_: URLCredential, for _: URLAuthenticationChallenge) {}
  func continueWithoutCredential(for _: URLAuthenticationChallenge) {}
  func cancel(_: URLAuthenticationChallenge) {}
  func performDefaultHandling(for _: URLAuthenticationChallenge) {}
  func rejectProtectionSpaceAndContinue(with _: URLAuthenticationChallenge) {}
}

private nonisolated final class ChallengeCapture: @unchecked Sendable {
  var disposition: URLSession.AuthChallengeDisposition?
  var credential: URLCredential?
}

final class MemorySessionStore: SessionStoring, @unchecked Sendable {
  private let lock = NSLock()
  private var stored: BoxSession?

  init(_ stored: BoxSession?) { self.stored = stored }

  func load() -> BoxSession? { lock.withLock { stored } }
  func save(_ session: BoxSession) throws { lock.withLock { stored = session } }
  func clear() throws { lock.withLock { stored = nil } }
}

final class RecordingKeyring: DeviceKeyring, @unchecked Sendable {
  static let spki = Data(RekeyProof.p256SPKIPrefix) + Data([0x04]) + Data(repeating: 0x22, count: 64)

  private let lock = NSLock()
  private var made = 0
  private var _installed: [(certificate: String, key: String)] = []
  private var _signed: [Data] = []
  private var _removed: [String] = []
  private var _removedAll = 0
  private var _refuseInstall = false
  private var _refuseRemoveAll = false

  var installed: [(certificate: String, key: String)] { lock.withLock { _installed } }
  var signed: [Data] { lock.withLock { _signed } }
  var removed: [String] { lock.withLock { _removed } }
  var removedAll: Int { lock.withLock { _removedAll } }
  var refuseInstall: Bool {
    get { lock.withLock { _refuseInstall } }
    set { lock.withLock { _refuseInstall = newValue } }
  }
  var refuseRemoveAll: Bool {
    get { lock.withLock { _refuseRemoveAll } }
    set { lock.withLock { _refuseRemoveAll = newValue } }
  }

  func installQRIdentity(certificatePEM: String, privateKeyPEM: String) throws -> String {
    try lock.withLock {
      if _refuseInstall { throw DeviceKeyError.badKey }
      _installed.append((certificatePEM, privateKeyPEM))
      return "qr-\(_installed.count)"
    }
  }

  func identity(tag _: String) throws -> SecIdentity {
    throw DeviceKeyError.identityNotFound // the model's tests connect through FakeBox
  }

  func makeKey() throws -> NewDeviceKey {
    lock.withLock {
      made += 1
      return NewDeviceKey(tag: "made-\(made)", spki: Self.spki)
    }
  }

  func sign(_ message: Data, tag _: String) throws -> Data {
    lock.withLock {
      _signed.append(message)
      return Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01])
    }
  }

  func installCertificate(pem _: String, tag _: String) throws {}

  func remove(tag: String) {
    lock.withLock { _removed.append(tag) }
  }

  func removeAll() throws {
    try lock.withLock {
      if _refuseRemoveAll { throw DeviceKeyError.keychain(errSecInteractionNotAllowed) }
      _removedAll += 1
    }
  }
}

/// The box as the model sees it: answers per identity tag, and a log of what was asked over which.
@MainActor
final class FakeBox {
  static let newCertificate = "-----BEGIN CERTIFICATE-----\n\(Data([0x30, 0x03, 0x02, 0x01, 0x02]).base64EncodedString())\n-----END CERTIFICATE-----\n"

  var polls: [UnlockPoll] = []
  var downFor: Set<String> = []
  var confirmFails = 0
  private(set) var calls: [String] = []
  private(set) var pins: [String] = []
  private(set) var rekeyed: [(spki: Data, signature: Data)] = []

  func over(tag: String, token: String?) -> any BoxCalls {
    Connection(box: self, tag: tag)
  }

  fileprivate func record(_ what: String, _ tag: String) throws {
    calls.append("\(what)@\(tag)")
    if downFor.contains(tag) { throw BoxChannelError.appearsDown }
  }

  fileprivate func nextPoll() -> UnlockPoll {
    polls.isEmpty ? UnlockPoll(stages: [], done: false, failed: nil, token: nil, expiresAt: nil) : polls.removeFirst()
  }

  fileprivate func notePin(_ pin: String) { pins.append(pin) }
  fileprivate func noteRekey(_ spki: Data, _ signature: Data) { rekeyed.append((spki, signature)) }
  fileprivate func takeConfirmFailure() -> Bool {
    guard confirmFails > 0 else { return false }
    confirmFails -= 1
    return true
  }

  private struct Connection: BoxCalls {
    let box: FakeBox
    let tag: String

    func unlockStart(pin: String) async throws -> String {
      try box.record("unlock", tag)
      box.notePin(pin)
      return "run-1"
    }

    func unlockPoll(run _: String) async throws -> UnlockPoll {
      try box.record("poll", tag)
      return box.nextPoll()
    }

    func status() async throws -> BoxStatus {
      try box.record("status", tag)
      return BoxStatus(services: [], datastores: nil)
    }

    func rekey(spki: Data, signature: Data) async throws -> RekeyAnswer {
      try box.record("rekey", tag)
      box.noteRekey(spki, signature)
      return RekeyAnswer(ok: true, cert: FakeBox.newCertificate, lifeDays: 14, renewAfterHours: 24)
    }

    func rekeyConfirm() async throws {
      try box.record("confirm", tag)
      if box.takeConfirmFailure() { throw BoxChannelError.unreachable("lost") }
    }
  }
}
