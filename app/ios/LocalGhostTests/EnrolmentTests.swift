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
  func rebuildsServerLGQR2FixtureFromDataAndParityFrames() throws {
    // Frames 0...4 plus parity frames 11...16 from server/internal/pair/qrstream_test.go.
    // This fixed cross-platform fixture catches drift in the GF(256), Cauchy-row and CRC contract.
    let encodedFrames = [
      "TEdRUjIgMCAxMSA2IDEyNDMgMzQ2YmNmODYgNzFiZSBsb2NhbGdob3N0Oi8vZW5yb2xsP0lnQ0JxMVkzJlYybyZzMTYyM0s3N1JHaWlPJUs/cElPMU5pS3NIJXhxUWRpXzNHNENGOjdFXy5PRDRfLXBKOUZBd3hZbEk6LWhaTlYwRjRzRWFhVm9jMkp5M0dGcA==",
      "TEdRUjIgMSAxMSA2IDEyNDMgMzQ2YmNmODYgYmNhNiBsZFVqJjVZakEyYTNfZkFCd0F6LWY6L1IybC9YJmhWNzJRbiVkOXFCM0ZBWXYvUHkmYXU1eWpuM1RNNTExYjZCT0tIdlQlNEFhP0hBPTE6Tz16UnBMND9WQTppMnBILm5FWHBQNVBub1Z5JUZDMndQLw==",
      "TEdRUjIgMiAxMSA2IDEyNDMgMzQ2YmNmODYgOGVlMSBVNVY2WGEyU0ZHZF9XPVgmLVhmald2cDBaR0VNZVAxREVfQ0tiZz9pOFQ6QWNCbXE9cDBXeEpkMUxnTUYtWHllQlYwbVgtJjo9Sy1Md1oycTBXPzlIemVmdEc5UVBlN1pSZ0tmL3UwP0laRFJDWkZIbA==",
      "TEdRUjIgMyAxMSA2IDEyNDMgMzQ2YmNmODYgYzNlZCB0VldHN0U9Ukt3P1BDWFhDZUNncUJkT2ZVdFgzfkR1cDM1U3pFYmFJRzdSYnFuNXl4MDNvQ3BpJkJETTpraTRqeXNQb2VtODNzSU9jOFhHOjglS3ozSk11bTNjajlHUGtwZzhEQ2V2TmJTcXpLZjlwOg==",
      "TEdRUjIgNCAxMSA2IDEyNDMgMzQ2YmNmODYgMTI5ZiB+QiVLeUlpcS5zMHg5Py5sVkRkP1ZtZi1kd1JFQ0Zyfmd2QTZ0cmNZMlc9ckJOV09WSjR+UXIvc1hSYURvNHpWajlyXyVRUTpmfkFZJXRrSXdCbnkyYlQvYU84PUVWLjpMamdLV1RPTkslNHc/P2lHNw==",
      "TEdRUjIgMTEgMTEgNiAxMjQzIDM0NmJjZjg2IDM2MmYgJ4/h8I7CpGTFig4ujnYjM8M3ySxsAt7C52tiybniUkEtpTgx9M6n7z8rzF7dWGgp0RYROT6cgv22QCQcLwTPr5jXizT65h3Y3rYg79gCG6XrLIaiivpBzgo39PZwgv9qDkHbDCFOn6PqnFQn0bTWwrg=",
      "TEdRUjIgMTIgMTEgNiAxMjQzIDM0NmJjZjg2IDI4MDgg5Vm21jnnYOtag9V21EtvfLYsdpWdX9Hn/GaGdX8B/2Z0OqzrdrnXu9VZBQGCE05FKig8QwEUJRR4Jf9mD95k2jqEpCzx01HyrCi9xJJHXBr+Bqz1mu0MAsLafj+eNYYJCvqBuj6rbpSDxXVT5LAlY+U=",
      "TEdRUjIgMTMgMTEgNiAxMjQzIDM0NmJjZjg2IDAxOTYgvecRr7jvi78RGGMlSe4sVDgXzh0WYXSiBY9srPc8WKtXEmpHBEnTrmP4UolfYuOnqNrEHku0PXYsR8K2rE83jjE5Cps9r2/CiFXDyOTBLBcHeHbqwPts3yFJKYKsqOrucK09+3xWhbcqU1lq1ppxNmg=",
      "TEdRUjIgMTQgMTEgNiAxMjQzIDM0NmJjZjg2IDA4MDUg7kE68anwTWXkND9CuQV/lSv+IGTqoOQSBhrYNyn17biqwT8v2TltCLPw/EZEUIuOriJv0jXm/pakec7OIloZiokRvIexmG3Y0nXL4ogSUNvql3xcq0ui7e1DruuUDsgwxpaQkxl4B/6pT3FaigsIIsc=",
      "TEdRUjIgMTUgMTEgNiAxMjQzIDM0NmJjZjg2IDU4YmEgPPQ/OHgcQpG9K9c92OWJee+HiojTmPQbrIhzXik01rEUMljZXX0SEzf21lzmQbErXtLDVkTUTWJtuz09n9tboio+m8bR8C4Zn0wpmILhYjz4pvIJJbgpa/VxhVhAFhJ19p1t5MALZKdxtTWdE+EIEL4=",
      "TEdRUjIgMTYgMTEgNiAxMjQzIDM0NmJjZjg2IDFiY2UgJvu6Y+1duwHBQjU4nKTnTEiTRSVP5v/OArh1ZLAY0imKSMtFCgrh45dzsf7IMq9kJgQKSK8W8+GBnhrRKnqA6EDZ4+Key55GK0WUv4GCe0eus8hdW/OiNr2Tsb2wGUnwSslMxrcBzI3pn3Vz0OevGUs=",
    ]
    let frames = try encodedFrames.map { encoded -> String in
      let data = try #require(Data(base64Encoded: encoded))
      return try #require(String(data: data, encoding: .isoLatin1))
    }
    var assembler = EnrolmentQRStreamAssembler()
    var payload: Data?

    for frame in frames {
      payload = assembler.offer(frame) ?? payload
    }

    let rebuilt = try #require(payload)
    #expect(rebuilt.count == 1_243)
    #expect(String(data: rebuilt, encoding: .isoLatin1)?.hasPrefix("localghost://enroll?") == true)
  }

  @Test
  func extractsBinaryByteModePayloadFromVisionCodewords() throws {
    // QR byte mode + 8-bit length 3 + [ff 00 80], followed by arbitrary data-codeword padding.
    var codewords = Data([0x40, 0x3f, 0xf0, 0x08, 0x00])
    codewords.append(Data(repeating: 0xec, count: 11))

    let payload = try #require(QRByteModePayload.decode(codewords))

    #expect(payload == Data([0xff, 0x00, 0x80]))
  }

  @Test
  @MainActor
  func setupStoresScannedCredentialsAtomically() throws {
    let store = RecordingCredentialStore()
    let keyring = RecordingKeyring()
    let model = SetupModel(credentialStore: store, keyring: keyring, deviceName: "My iPhone")
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
    // the key went into the Keychain as a key; the record keeps its tag and not the key
    #expect(stored?.devicePrivateKeyPEM == nil)
    #expect(stored?.keyTag == "qr-1")
    #expect(keyring.installed.count == 1)
    #expect(keyring.installed.first?.key == privateKey)
    #expect(keyring.installed.first?.certificate == certificate)
  }

  @Test
  @MainActor
  func aKeyThatCannotBeInstalledLeavesNothingStored() throws {
    let store = RecordingCredentialStore()
    let keyring = RecordingKeyring()
    keyring.refuseInstall = true
    let model = SetupModel(credentialStore: store, keyring: keyring, deviceName: "My iPhone")
    let certificate = "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n"
    let privateKey = "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n"
    let raw = "localghost://enroll?v=2&host=box.local&fp=\(fingerprint)&cert=\(Data(certificate.utf8).base64URLString)&key=\(Data(privateKey.utf8).base64URLString)"

    #expect(model.acceptEnrolmentPayload(raw))
    #expect(!model.pair())
    #expect(store.credentials == nil)
    #expect(model.errorMessage != nil)
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
