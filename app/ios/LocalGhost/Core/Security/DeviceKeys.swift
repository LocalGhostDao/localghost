import CryptoKit
import Foundation
import Security

// THE DEVICE'S KEYS. The QR carries a certificate the box issued AND its private key: the box made
// the key, and anyone who photographed the QR holds a copy. So the key is put where TLS can use it
// (a Keychain key beside its certificate, which the Keychain pairs into an identity), and after the
// first unlock the phone makes a key of its own in the Secure Enclave, proves it holds it, and the
// box signs a certificate for that one (server/internal/secd/rekey.go, the same dance as Android's
// DeviceCert). The QR's certificate is then retired on the box and its key deleted here. The same
// again once a day, so a phone in use never reaches the end of its two-week certificate.

/// A key made on this phone for a rekey: where it lives, and its public half as the box reads it.
nonisolated struct NewDeviceKey: Sendable {
  let tag: String
  let spki: Data
}

protocol DeviceKeyring: Sendable {
  /// Puts the QR's certificate and key into the Keychain as an identity; returns the key's tag.
  func installQRIdentity(certificatePEM: String, privateKeyPEM: String) throws -> String
  /// The identity (key and certificate) whose key has this tag, for TLS.
  func identity(tag: String) throws -> SecIdentity
  /// A new key, in the Secure Enclave when the phone has one.
  func makeKey() throws -> NewDeviceKey
  /// An ECDSA P-256 signature (DER) over SHA-256 of the message, with the key that has this tag.
  func sign(_ message: Data, tag: String) throws -> Data
  /// Keeps a certificate the box signed for the key with this tag.
  func installCertificate(pem: String, tag: String) throws
  /// Deletes one key and its certificate.
  func remove(tag: String)
  /// Deletes every key and certificate this app holds.
  func removeAll() throws
}

enum DeviceKeyError: Error, Equatable, LocalizedError {
  case badKey
  case badCertificate
  case keyNotFound
  case identityNotFound
  case keychain(OSStatus)
  case crypto(String)

  var errorDescription: String? {
    switch self {
    case .badKey: "The device key in the code could not be read."
    case .badCertificate: "The device certificate could not be read."
    case .keyNotFound: "This iPhone's device key is missing."
    case .identityNotFound: "This iPhone's device key and certificate do not belong together."
    case .keychain(let status): "Secure storage failed (Keychain status \(status))."
    case .crypto(let what): "The device key could not be used (\(what))."
    }
  }
}

struct KeychainDeviceKeyring: DeviceKeyring {
  private let tagPrefix = "ai.localghost.device."

  func installQRIdentity(certificatePEM: String, privateKeyPEM: String) throws -> String {
    guard let pkcs8 = PEM.der(from: privateKeyPEM, type: "PRIVATE KEY") else {
      throw DeviceKeyError.badKey
    }
    // the box writes PKCS#8 (Go's x509.MarshalPKCS8PrivateKey); the Keychain takes X9.63
    // (04 || X || Y || D), which CryptoKit reads the one into and writes the other out of
    let key: P256.Signing.PrivateKey
    do {
      key = try P256.Signing.PrivateKey(derRepresentation: pkcs8)
    } catch {
      throw DeviceKeyError.badKey
    }
    let attributes: [String: Any] = [
      kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
      kSecAttrKeyClass as String: kSecAttrKeyClassPrivate,
      kSecAttrKeySizeInBits as String: 256,
    ]
    var error: Unmanaged<CFError>?
    guard let secKey = SecKeyCreateWithData(key.x963Representation as CFData, attributes as CFDictionary, &error) else {
      throw DeviceKeyError.crypto(Self.describe(error))
    }
    let tag = tagPrefix + UUID().uuidString
    let add: [String: Any] = [
      kSecClass as String: kSecClassKey,
      kSecValueRef as String: secKey,
      kSecAttrApplicationTag as String: Data(tag.utf8),
      kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
      kSecAttrSynchronizable as String: false,
    ]
    let status = SecItemAdd(add as CFDictionary, nil)
    guard status == errSecSuccess else {
      throw DeviceKeyError.keychain(status)
    }
    do {
      try installCertificate(pem: certificatePEM, tag: tag)
    } catch {
      remove(tag: tag)
      throw error
    }
    return tag
  }

  func identity(tag: String) throws -> SecIdentity {
    // The Keychain pairs a certificate with the key whose public half it carries. The identity is
    // found by that public half: every identity this app holds is read and the one whose key
    // matches the tagged key's is the one (there are two at most, during a rekey).
    let want = try Self.publicKeyBytes(of: privateKey(tag: tag))
    let query: [String: Any] = [
      kSecClass as String: kSecClassIdentity,
      kSecReturnRef as String: true,
      kSecMatchLimit as String: kSecMatchLimitAll,
    ]
    var result: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &result)
    if status == errSecItemNotFound {
      throw DeviceKeyError.identityNotFound
    }
    guard status == errSecSuccess, let identities = result as? [SecIdentity] else {
      throw DeviceKeyError.keychain(status)
    }
    for identity in identities {
      var key: SecKey?
      guard SecIdentityCopyPrivateKey(identity, &key) == errSecSuccess, let key else { continue }
      if let have = try? Self.publicKeyBytes(of: key), have == want {
        return identity
      }
    }
    throw DeviceKeyError.identityNotFound
  }

  func makeKey() throws -> NewDeviceKey {
    let tag = tagPrefix + UUID().uuidString
    var privateAttributes: [String: Any] = [
      kSecAttrIsPermanent as String: true,
      kSecAttrApplicationTag as String: Data(tag.utf8),
    ]
    var attributes: [String: Any] = [
      kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
      kSecAttrKeySizeInBits as String: 256,
    ]
    if SecureEnclave.isAvailable {
      // made inside the Secure Enclave and never leaves it: usable while the phone is unlocked
      var acError: Unmanaged<CFError>?
      guard let access = SecAccessControlCreateWithFlags(
        nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, .privateKeyUsage, &acError
      ) else {
        throw DeviceKeyError.crypto(Self.describe(acError))
      }
      privateAttributes[kSecAttrAccessControl as String] = access
      attributes[kSecAttrTokenID as String] = kSecAttrTokenIDSecureEnclave
    } else {
      // the simulator has no Secure Enclave: a Keychain key on this device only
      privateAttributes[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    }
    attributes[kSecPrivateKeyAttrs as String] = privateAttributes
    var error: Unmanaged<CFError>?
    guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &error) else {
      throw DeviceKeyError.crypto(Self.describe(error))
    }
    let point = try Self.publicKeyBytes(of: key)
    guard let spki = RekeyProof.spki(x963PublicKey: point) else {
      remove(tag: tag)
      throw DeviceKeyError.crypto("public key")
    }
    return NewDeviceKey(tag: tag, spki: spki)
  }

  func sign(_ message: Data, tag: String) throws -> Data {
    let key = try privateKey(tag: tag)
    var error: Unmanaged<CFError>?
    guard let signature = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, message as CFData, &error) else {
      throw DeviceKeyError.crypto(Self.describe(error))
    }
    return signature as Data
  }

  func installCertificate(pem: String, tag: String) throws {
    guard let der = PEM.der(from: pem, type: "CERTIFICATE"),
          let certificate = SecCertificateCreateWithData(nil, der as CFData)
    else {
      throw DeviceKeyError.badCertificate
    }
    let add: [String: Any] = [
      kSecClass as String: kSecClassCertificate,
      kSecValueRef as String: certificate,
      kSecAttrLabel as String: Self.certificateLabel(tag),
      kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
      kSecAttrSynchronizable as String: false,
    ]
    let status = SecItemAdd(add as CFDictionary, nil)
    guard status == errSecSuccess || status == errSecDuplicateItem else {
      throw DeviceKeyError.keychain(status)
    }
  }

  func remove(tag: String) {
    _ = SecItemDelete([
      kSecClass as String: kSecClassKey,
      kSecAttrApplicationTag as String: Data(tag.utf8),
    ] as CFDictionary)
    _ = SecItemDelete([
      kSecClass as String: kSecClassCertificate,
      kSecAttrLabel as String: Self.certificateLabel(tag),
    ] as CFDictionary)
  }

  func removeAll() throws {
    // this app's Keychain holds nothing else of these classes: every key and certificate goes
    for itemClass in [kSecClassKey, kSecClassCertificate] {
      let status = SecItemDelete([kSecClass as String: itemClass] as CFDictionary)
      guard status == errSecSuccess || status == errSecItemNotFound else {
        throw DeviceKeyError.keychain(status)
      }
    }
  }

  private func privateKey(tag: String) throws -> SecKey {
    let query: [String: Any] = [
      kSecClass as String: kSecClassKey,
      kSecAttrApplicationTag as String: Data(tag.utf8),
      kSecAttrKeyClass as String: kSecAttrKeyClassPrivate,
      kSecReturnRef as String: true,
      kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var result: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &result)
    if status == errSecItemNotFound {
      throw DeviceKeyError.keyNotFound
    }
    guard status == errSecSuccess, let result, CFGetTypeID(result) == SecKeyGetTypeID() else {
      throw DeviceKeyError.keychain(status)
    }
    return result as! SecKey // swiftlint:disable:this force_cast , the type was checked above
  }

  private static func publicKeyBytes(of privateKey: SecKey) throws -> Data {
    guard let publicKey = SecKeyCopyPublicKey(privateKey) else {
      throw DeviceKeyError.crypto("no public key")
    }
    var error: Unmanaged<CFError>?
    guard let bytes = SecKeyCopyExternalRepresentation(publicKey, &error) else {
      throw DeviceKeyError.crypto(describe(error))
    }
    return bytes as Data
  }

  private static func certificateLabel(_ tag: String) -> String { "cert:" + tag }

  private static func describe(_ error: Unmanaged<CFError>?) -> String {
    guard let error = error?.takeRetainedValue() else { return "unknown" }
    return (error as Error).localizedDescription
  }
}

/// PEM to DER: the base64 between the BEGIN and END lines of the named type.
nonisolated enum PEM {
  static func der(from pem: String, type: String) -> Data? {
    let begin = "-----BEGIN \(type)-----"
    let end = "-----END \(type)-----"
    guard let start = pem.range(of: begin), let stop = pem.range(of: end, range: start.upperBound..<pem.endIndex) else {
      return nil
    }
    let body = pem[start.upperBound..<stop.lowerBound].filter { !$0.isWhitespace }
    guard let der = Data(base64Encoded: String(body)), !der.isEmpty else { return nil }
    return der
  }
}

/// What the box checks before it signs a certificate for a key the phone made: the key's public
/// half (a P-256 SubjectPublicKeyInfo), and a signature with it over the message below, which binds
/// the proof to the certificate presented (rekey.go, rekeyMessageV2).
nonisolated enum RekeyProof {
  /// SubjectPublicKeyInfo for id-ecPublicKey on prime256v1, up to the 65-byte point.
  static let p256SPKIPrefix: [UInt8] = [
    0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
    0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
  ]

  /// The SPKI for an uncompressed P-256 point (04 || X || Y), nil for anything else.
  static func spki(x963PublicKey point: Data) -> Data? {
    guard point.count == 65, point.first == 0x04 else { return nil }
    return Data(p256SPKIPrefix) + point
  }

  /// "localghost rekey v2\n" + hex SHA-256 of the presented certificate's DER + "\n" + the SPKI.
  static func message(presentedCertificateDER der: Data, spki: Data) -> Data {
    let digest = SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
    return Data("localghost rekey v2\n\(digest)\n".utf8) + spki
  }
}

/// The session the box hands over at the end of an unlock: the bearer for every call after it,
/// good for two days. Kept beside the device's other secrets, on this device only.
nonisolated struct BoxSession: Codable, Equatable, Sendable {
  let token: String
  let expiresAt: Date

  func isValid(at now: Date) -> Bool { expiresAt > now.addingTimeInterval(60) }
}

protocol SessionStoring: Sendable {
  func load() -> BoxSession?
  func save(_ session: BoxSession) throws
  func clear() throws
}

struct KeychainSessionStore: SessionStoring {
  private let service: String
  private let account = "box-session"

  init(service: String = "ai.localghost.app.session") {
    self.service = service
  }

  func load() -> BoxSession? {
    var query = baseQuery
    query[kSecReturnData as String] = true
    query[kSecMatchLimit as String] = kSecMatchLimitOne
    var result: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess, let data = result as? Data else {
      return nil
    }
    return try? JSONDecoder().decode(BoxSession.self, from: data)
  }

  func save(_ session: BoxSession) throws {
    let data = try JSONEncoder().encode(session)
    try? clear()
    var attributes = baseQuery
    attributes[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    attributes[kSecValueData as String] = data
    let status = SecItemAdd(attributes as CFDictionary, nil)
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

/// Keychain items outlive the app on iOS; UserDefaults does not. So the first launch after an
/// install finds no mark, and whatever the Keychain still holds from an earlier install (a device
/// key, its certificate, a session) is deleted before anything reads it: deleting the app deletes
/// the box's key with it, as it would on Android. The mark is set only once the clearing worked,
/// so a clearing that failed is tried again at the next launch.
enum InstallMark {
  static let key = "ai.localghost.installed"

  @discardableResult
  static func clearLeftovers(
    defaults: UserDefaults,
    credentials: any CredentialStoring,
    keyring: any DeviceKeyring,
    sessions: any SessionStoring
  ) -> Bool {
    guard !defaults.bool(forKey: key) else { return false }
    do {
      try credentials.clear()
      try keyring.removeAll()
      try sessions.clear()
    } catch {
      return false
    }
    defaults.set(true, forKey: key)
    return true
  }
}
