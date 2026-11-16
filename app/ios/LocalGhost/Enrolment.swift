import Foundation
import Observation
import Security
import UIKit

struct EnrolmentLink: Equatable, Sendable {
  static let currentVersion = 3

  let host: String
  let port: Int
  let pairingCode: String
  let certificateFingerprint: String
  let boxName: String
  let version: Int
  let deviceCertificatePEM: String?
  let devicePrivateKeyPEM: String?

  var baseURL: String {
    var components = URLComponents()
    components.scheme = "https"
    components.host = host
    components.port = port
    return components.url?.absoluteString ?? "https://\(host):\(port)"
  }

  static func parse(_ rawValue: String) throws -> EnrolmentLink {
    let value = rawValue.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let components = URLComponents(string: value),
          components.scheme?.lowercased() == "localghost",
          components.host?.lowercased() == "enroll" // Existing box wire-format contract.
    else {
      throw EnrolmentLinkError.notEnrolmentLink
    }

    let parameters = Dictionary(
      components.queryItems?.map { ($0.name, $0.value ?? "") } ?? [],
      uniquingKeysWith: { _, newest in newest }
    )

    let version: Int
    if let rawVersion = parameters["v"] {
      guard let parsedVersion = Int(rawVersion) else {
        throw EnrolmentLinkError.malformed
      }
      version = parsedVersion
    } else {
      version = 1
    }

    guard version <= currentVersion else {
      throw EnrolmentLinkError.newerVersion(version)
    }

    guard let host = parameters["host"]?.trimmingCharacters(in: .whitespacesAndNewlines),
          !host.isEmpty,
          let rawFingerprint = parameters["fp"]?.trimmingCharacters(in: .whitespacesAndNewlines),
          let fingerprint = normalizedFingerprint(rawFingerprint)
    else {
      throw EnrolmentLinkError.malformed
    }

    let port = parameters["port"].flatMap(Int.init) ?? 8443
    guard (1...65_535).contains(port) else {
      throw EnrolmentLinkError.malformed
    }

    let pairingCode = parameters["code"]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    let certificate = pemValue(
      derValue: parameters["certder"],
      pemValue: parameters["cert"],
      type: "CERTIFICATE"
    )
    let privateKey = pemValue(
      derValue: parameters["keyder"],
      pemValue: parameters["key"],
      type: "PRIVATE KEY"
    )

    if version >= 2 {
      guard certificate?.isEmpty == false else {
        throw EnrolmentLinkError.malformed
      }
    } else if pairingCode.isEmpty {
      throw EnrolmentLinkError.malformed
    }

    return EnrolmentLink(
      host: host,
      port: port,
      pairingCode: pairingCode,
      certificateFingerprint: fingerprint,
      boxName: parameters["name"]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
      version: version,
      deviceCertificatePEM: certificate,
      devicePrivateKeyPEM: privateKey
    )
  }

  private static func normalizedFingerprint(_ value: String) -> String? {
    guard value.allSatisfy({ character in
      character.isHexDigit || character == ":" || character == "-" || character.isWhitespace
    }) else {
      return nil
    }
    let hex = value.uppercased().filter(\.isHexDigit)
    guard hex.count == 64 else { return nil }
    return stride(from: 0, to: hex.count, by: 2)
      .map { offset in
        let start = hex.index(hex.startIndex, offsetBy: offset)
        let end = hex.index(start, offsetBy: 2)
        return String(hex[start..<end])
      }
      .joined(separator: ":")
  }

  private static func pemValue(
    derValue: String?,
    pemValue: String?,
    type: String
  ) -> String? {
    if let derValue, let der = Data(base64URLEncoded: derValue), !der.isEmpty {
      let encoded = der.base64EncodedString()
      let lines = stride(from: 0, to: encoded.count, by: 64).map { offset in
        let start = encoded.index(encoded.startIndex, offsetBy: offset)
        let end = encoded.index(start, offsetBy: min(64, encoded.count - offset))
        return String(encoded[start..<end])
      }
      return (["-----BEGIN \(type)-----"] + lines + ["-----END \(type)-----", ""])
        .joined(separator: "\n")
    }

    guard let pemValue,
          let decoded = Data(base64URLEncoded: pemValue),
          let pem = String(data: decoded, encoding: .utf8),
          !pem.isEmpty
    else {
      return nil
    }
    return pem
  }
}

enum EnrolmentLinkError: Error, Equatable, LocalizedError {
  case notEnrolmentLink
  case malformed
  case newerVersion(Int)

  var errorDescription: String? {
    switch self {
    case .notEnrolmentLink:
      "This is not a LocalGhost enrolment code."
    case .malformed:
      "The enrolment code is incomplete or invalid."
    case .newerVersion:
      "This code was created by a newer version of LocalGhost. Update the app and try again."
    }
  }
}

private extension Data {
  init?(base64URLEncoded value: String) {
    var base64 = value
      .trimmingCharacters(in: .whitespacesAndNewlines)
      .replacingOccurrences(of: "-", with: "+")
      .replacingOccurrences(of: "_", with: "/")
    let padding = (4 - base64.count % 4) % 4
    base64.append(String(repeating: "=", count: padding))
    self.init(base64Encoded: base64)
  }
}

struct EnrolmentCredentials: Codable, Equatable, Sendable {
  let baseURL: String
  let deviceName: String
  let certificateFingerprint: String
  let deviceCertificatePEM: String
  let devicePrivateKeyPEM: String
}

protocol CredentialStoring: Sendable {
  func load() throws -> EnrolmentCredentials?
  func save(_ credentials: EnrolmentCredentials) throws
  func clear() throws
}

struct KeychainCredentialStore: CredentialStoring {
  private let service: String
  private let account = "box-enrolment"

  init(service: String = "ai.localghost.app.credentials") {
    self.service = service
  }

  func load() throws -> EnrolmentCredentials? {
    var query = baseQuery
    query[kSecReturnData as String] = true
    query[kSecMatchLimit as String] = kSecMatchLimitOne

    var result: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &result)
    if status == errSecItemNotFound {
      return nil
    }
    guard status == errSecSuccess, let data = result as? Data else {
      throw KeychainCredentialStoreError.unexpectedStatus(status)
    }
    return try JSONDecoder().decode(EnrolmentCredentials.self, from: data)
  }

  func save(_ credentials: EnrolmentCredentials) throws {
    let data = try JSONEncoder().encode(credentials)
    var attributes = baseQuery
    attributes[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    attributes[kSecAttrSynchronizable as String] = false
    attributes[kSecValueData as String] = data

    let status = SecItemAdd(attributes as CFDictionary, nil)
    if status == errSecDuplicateItem {
      let updateStatus = SecItemUpdate(
        baseQuery as CFDictionary,
        [kSecValueData as String: data] as CFDictionary
      )
      guard updateStatus == errSecSuccess else {
        throw KeychainCredentialStoreError.unexpectedStatus(updateStatus)
      }
      return
    }
    guard status == errSecSuccess else {
      throw KeychainCredentialStoreError.unexpectedStatus(status)
    }
  }

  func clear() throws {
    let status = SecItemDelete(baseQuery as CFDictionary)
    guard status == errSecSuccess || status == errSecItemNotFound else {
      throw KeychainCredentialStoreError.unexpectedStatus(status)
    }
  }

  private var baseQuery: [String: Any] {
    [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: account,
      kSecAttrSynchronizable as String: false,
    ]
  }
}

enum KeychainCredentialStoreError: Error, Equatable, LocalizedError {
  case unexpectedStatus(OSStatus)

  var errorDescription: String? {
    switch self {
    case .unexpectedStatus(let status):
      "Secure storage failed (Keychain status \(status))."
    }
  }
}

@MainActor
@Observable
final class SetupModel {
  var boxAddress = ""
  var deviceName: String
  private(set) var boxName = ""
  private(set) var fingerprint = ""
  private(set) var scannedLink: EnrolmentLink?
  private(set) var errorMessage: String?

  private let credentialStore: any CredentialStoring

  init(
    credentialStore: any CredentialStoring,
    deviceName: String = UIDevice.current.name
  ) {
    self.credentialStore = credentialStore
    self.deviceName = deviceName
  }

  var canPair: Bool {
    scannedLink != nil
      && !boxAddress.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
      && !deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  }

  @discardableResult
  func acceptEnrolmentPayload(_ payload: String) -> Bool {
    do {
      let link = try EnrolmentLink.parse(payload)
      guard link.deviceCertificatePEM?.isEmpty == false,
            link.devicePrivateKeyPEM?.isEmpty == false
      else {
        throw SetupError.missingCredentials
      }
      scannedLink = link
      boxAddress = link.baseURL
      boxName = link.boxName
      fingerprint = link.certificateFingerprint
      errorMessage = nil
      return true
    } catch {
      errorMessage = error.localizedDescription
      return false
    }
  }

  @discardableResult
  func pair() -> Bool {
    do {
      guard let link = scannedLink,
            let certificate = link.deviceCertificatePEM,
            let privateKey = link.devicePrivateKeyPEM
      else {
        throw SetupError.scanRequired
      }

      let address = boxAddress.trimmingCharacters(in: .whitespacesAndNewlines)
      guard let components = URLComponents(string: address),
            components.scheme?.lowercased() == "https",
            components.host?.isEmpty == false,
            components.user == nil,
            components.password == nil,
            components.query == nil,
            components.fragment == nil
      else {
        throw SetupError.invalidAddress
      }

      let name = deviceName.trimmingCharacters(in: .whitespacesAndNewlines)
      guard !name.isEmpty else {
        throw SetupError.missingDeviceName
      }

      try credentialStore.save(
        EnrolmentCredentials(
          baseURL: address.trimmingCharacters(in: CharacterSet(charactersIn: "/")),
          deviceName: name,
          certificateFingerprint: link.certificateFingerprint,
          deviceCertificatePEM: certificate,
          devicePrivateKeyPEM: privateKey
        )
      )
      scannedLink = nil
      errorMessage = nil
      return true
    } catch {
      errorMessage = error.localizedDescription
      return false
    }
  }
}

enum SetupError: Error, Equatable, LocalizedError {
  case scanRequired
  case missingCredentials
  case invalidAddress
  case missingDeviceName

  var errorDescription: String? {
    switch self {
    case .scanRequired:
      "Scan the enrolment QR before pairing this device."
    case .missingCredentials:
      "This QR has no device certificate. Regenerate it on the box and scan again."
    case .invalidAddress:
      "Enter a valid HTTPS address for the box."
    case .missingDeviceName:
      "Enter a name for this device."
    }
  }
}
