import Foundation
import Testing

@testable import LocalGhost

struct EnrolmentLinkTests {
  private let fingerprint = String(repeating: "AB", count: 32)

  @Test
  func parsesVersionThreeDERCredentials() throws {
    let certificate = Data([0x30, 0x01, 0x02]).base64URLString
    let privateKey = Data([0x30, 0x03, 0x04]).base64URLString
    let raw = "localghost://enroll?v=3&host=box.local&port=9443&fp=\(fingerprint)&name=My%20Box&certder=\(certificate)&keyder=\(privateKey)"

    let link = try EnrolmentLink.parse(raw)

    #expect(link.host == "box.local")
    #expect(link.port == 9443)
    #expect(link.boxName == "My Box")
    #expect(link.baseURL == "https://box.local:9443")
    #expect(link.certificateFingerprint.split(separator: ":").count == 32)
    #expect(link.deviceCertificatePEM?.contains("BEGIN CERTIFICATE") == true)
    #expect(link.devicePrivateKeyPEM?.contains("BEGIN PRIVATE KEY") == true)
  }

  @Test
  func rejectsLinksFromANewerVersion() {
    let raw = "localghost://enroll?v=4&host=box.local&fp=\(fingerprint)"

    #expect(throws: EnrolmentLinkError.newerVersion(4)) {
      try EnrolmentLink.parse(raw)
    }
  }

  @Test
  @MainActor
  func setupStoresScannedCredentialsAtomically() throws {
    let store = RecordingCredentialStore()
    let model = SetupModel(credentialStore: store, deviceName: "My iPhone")
    let certificate = "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n"
    let privateKey = "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n"
    let raw = "localghost://enroll?v=2&host=box.local&fp=\(fingerprint)&cert=\(Data(certificate.utf8).base64URLString)&key=\(Data(privateKey.utf8).base64URLString)"

    #expect(model.acceptEnrolmentPayload(raw))
    model.boxAddress = "https://vpn.example:8443"
    #expect(model.pair())

    let stored = store.credentials
    #expect(stored?.baseURL == "https://vpn.example:8443")
    #expect(stored?.deviceName == "My iPhone")
    #expect(stored?.deviceCertificatePEM == certificate)
    #expect(stored?.devicePrivateKeyPEM == privateKey)
  }

  @Test
  func keychainRoundTrip() throws {
    let store = KeychainCredentialStore(service: "ai.localghost.tests.\(UUID().uuidString)")
    let credentials = EnrolmentCredentials(
      baseURL: "https://box.local:8443",
      deviceName: "Test iPhone",
      certificateFingerprint: fingerprint,
      deviceCertificatePEM: "certificate",
      devicePrivateKeyPEM: "private-key"
    )
    defer { try? store.clear() }

    try store.save(credentials)

    #expect(try store.load() == credentials)
    try store.clear()
    #expect(try store.load() == nil)
  }
}

private extension Data {
  var base64URLString: String {
    base64EncodedString()
      .replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_")
      .replacingOccurrences(of: "=", with: "")
  }
}

private final class RecordingCredentialStore: CredentialStoring, @unchecked Sendable {
  private let lock = NSLock()
  private var storedCredentials: EnrolmentCredentials?

  var credentials: EnrolmentCredentials? {
    lock.withLock { storedCredentials }
  }

  func load() throws -> EnrolmentCredentials? {
    lock.withLock { storedCredentials }
  }

  func save(_ credentials: EnrolmentCredentials) throws {
    lock.withLock { storedCredentials = credentials }
  }

  func clear() throws {
    lock.withLock { storedCredentials = nil }
  }
}
