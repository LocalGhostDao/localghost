import AVFoundation
import CoreLocation
import Foundation
import Observation
import UIKit

nonisolated enum AppPermission: String, CaseIterable, Identifiable, Sendable {
  case camera
  case location
  case localNetwork

  var id: Self { self }
  var title: String {
    switch self {
    case .camera: "Camera"
    case .location: "Location while using the app"
    case .localNetwork: "Local network"
    }
  }
  var reason: String {
    switch self {
    case .camera: "Scan the enrolment QR on your box. Camera images are not sent to the box or saved."
    case .location: "Ask your box for weather near you, to roughly a kilometre. Coordinates stay in memory and go only to your box. No background trail is recorded."
    case .localNetwork: "Reach your paired box on your network. iOS asks when a local connection needs it."
    }
  }
}

nonisolated enum PermissionAccess: Equatable, Sendable {
  case notAsked
  case allowed
  case approximate
  case denied
  case restricted
  case unavailable
  case systemSettings

  var label: String {
    switch self {
    case .notAsked: "Not asked"
    case .allowed: "Allowed"
    case .approximate: "Allowed · approximate"
    case .denied: "Denied"
    case .restricted: "Restricted"
    case .unavailable: "Unavailable"
    case .systemSettings: "Managed by iOS"
    }
  }
  var action: String? {
    switch self {
    case .notAsked: "Allow"
    case .denied, .allowed, .approximate, .systemSettings: "Open app settings"
    case .restricted, .unavailable: nil
    }
  }

  static func camera(_ status: AVAuthorizationStatus, available: Bool) -> Self {
    if status == .restricted { return .restricted }
    guard available else { return .unavailable }
    switch status {
    case .notDetermined: return .notAsked
    case .authorized: return .allowed
    case .denied: return .denied
    case .restricted: return .restricted
    @unknown default: return .restricted
    }
  }

  static func location(_ status: CLAuthorizationStatus, accuracy: CLAccuracyAuthorization) -> Self {
    switch status {
    case .notDetermined: return .notAsked
    case .authorizedAlways, .authorizedWhenInUse:
      return accuracy == .reducedAccuracy ? .approximate : .allowed
    case .denied: return .denied
    case .restricted: return .restricted
    @unknown default: return .restricted
    }
  }
}

/// Closures keep permission routing testable without displaying an OS prompt in tests.
struct PermissionClient {
  let status: @MainActor (AppPermission) -> PermissionAccess
  let request: @MainActor (AppPermission) async -> Void
  let openSettings: @MainActor () async -> Bool

  static func live(location: HomeLocationSource) -> Self {
    Self(
      status: { permission in
        switch permission {
        case .camera:
          return .camera(AVCaptureDevice.authorizationStatus(for: .video),
                         available: UIImagePickerController.isSourceTypeAvailable(.camera))
        case .location:
          return .location(location.authorizationStatus, accuracy: location.accuracyAuthorization)
        case .localNetwork:
          // A successful box request is not an OS permission query. Do not infer this grant.
          return .systemSettings
        }
      },
      request: { permission in
        switch permission {
        case .camera: _ = await AVCaptureDevice.requestAccess(for: .video)
        case .location: location.requestPermission()
        case .localNetwork: break
        }
      },
      openSettings: {
        guard let url = URL(string: UIApplication.openSettingsURLString) else { return false }
        return await UIApplication.shared.open(url)
      }
    )
  }
}

@MainActor
@Observable
final class PermissionsModel {
  private let client: PermissionClient
  private var revision = 0
  private(set) var asking: AppPermission?
  private(set) var errorMessage: String?

  init(client: PermissionClient) { self.client = client }

  func access(for permission: AppPermission) -> PermissionAccess {
    _ = revision
    return client.status(permission)
  }

  func refresh() { revision += 1 }

  func perform(_ permission: AppPermission) async {
    guard asking == nil else { return }
    let access = access(for: permission)
    guard access.action != nil else { return }
    asking = permission
    errorMessage = nil
    defer { asking = nil; refresh() }
    if access == .notAsked {
      await client.request(permission)
    } else {
      await openSettings()
    }
  }

  func openSettings() async {
    if !(await client.openSettings()) {
      errorMessage = "App settings could not open. Open Settings on this iPhone and choose LocalGhost."
    }
  }
}
