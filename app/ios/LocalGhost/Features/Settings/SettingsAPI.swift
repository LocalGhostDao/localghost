import Foundation
import Security

nonisolated struct BoxLockAnswer: Decodable, Sendable {
  let locked: Bool
  let error: String?
}

protocol SettingsServing: Sendable {
  func lockBox() async throws -> BoxLockAnswer
}

extension BoxClient: SettingsServing {
  func lockBox() async throws -> BoxLockAnswer {
    try await post("/v1/lock", body: [String: String]())
  }
}

/// Only the public part of the enrolment crosses into Settings. Never the bearer or private key.
struct BoxSettingsSnapshot {
  let address: String
  let deviceName: String
  let fingerprint: String
  let sessionEnds: Date?
  let keyMade: Date?
  let certificateEnds: Date?
  let pendingKey: Bool

  init(credentials: EnrolmentCredentials, session: BoxSession?) {
    address = credentials.baseURL
    deviceName = credentials.deviceName
    fingerprint = credentials.certificateFingerprint
    sessionEnds = session?.expiresAt.addingTimeInterval(-60)
    keyMade = credentials.rekeyedAt
    pendingKey = credentials.pendingKeyTag != nil
    if #available(iOS 18.0, *),
       let der = PEM.der(from: credentials.deviceCertificatePEM, type: "CERTIFICATE"),
       let certificate = SecCertificateCreateWithData(nil, der as CFData) {
      certificateEnds = SecCertificateCopyNotValidAfterDate(certificate) as Date?
    } else {
      certificateEnds = nil
    }
  }
}

enum SettingsError: LocalizedError {
  case lockRefused(String?)

  var errorDescription: String? {
    switch self {
    case .lockRefused(let reason):
      "The box could not lock. Its drive may still be open."
        + (reason.flatMap { $0.isEmpty ? nil : " \($0)" } ?? " Check Box Status before trying again.")
    }
  }
}
