@preconcurrency import CoreLocation
import Observation

/// Keeps only the latest coarse fix in memory. Home sends it only to the paired box for weather.
/// Initialising the manager or viewing Permissions must never start collection or show a prompt.
@MainActor
@Observable
final class HomeLocationSource: NSObject, CLLocationManagerDelegate {
  private(set) var latest: HomeLocation?
  private(set) var authorizationStatus: CLAuthorizationStatus = .notDetermined
  private(set) var accuracyAuthorization: CLAccuracyAuthorization = .reducedAccuracy

  @ObservationIgnored private let manager = CLLocationManager()
  @ObservationIgnored private var enabled = true
  @ObservationIgnored private var started = false

  override init() {
    super.init()
    authorizationStatus = manager.authorizationStatus
    accuracyAuthorization = manager.accuracyAuthorization
    manager.delegate = self
    manager.desiredAccuracy = kCLLocationAccuracyKilometer
  }

  func setEnabled(_ value: Bool) {
    enabled = value
    if !value {
      latest = nil
      manager.stopUpdatingLocation()
    } else if started {
      start()
    }
  }

  func start() {
    started = true
    guard enabled else { return }
    updateAuthorization()
    if authorizationStatus == .notDetermined {
      requestPermission()
    } else {
      requestFixIfAllowed()
    }
  }

  func requestPermission() {
    if manager.authorizationStatus == .notDetermined {
      manager.requestWhenInUseAuthorization()
    }
  }

  func latestAndRefresh() -> HomeLocation? {
    updateAuthorization()
    guard enabled, started, authorised else {
      latest = nil
      return nil
    }
    manager.requestLocation()
    return latest
  }

  private var authorised: Bool {
    authorizationStatus == .authorizedAlways || authorizationStatus == .authorizedWhenInUse
  }

  private func updateAuthorization() {
    authorizationStatus = manager.authorizationStatus
    accuracyAuthorization = manager.accuracyAuthorization
    if !authorised { latest = nil }
  }

  private func requestFixIfAllowed() {
    guard enabled, started, authorised else { return }
    manager.requestLocation()
  }

  func locationManagerDidChangeAuthorization(_: CLLocationManager) {
    updateAuthorization()
    requestFixIfAllowed()
  }

  func locationManager(_: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
    updateAuthorization()
    guard enabled, started, authorised,
          let location = locations.last, location.horizontalAccuracy >= 0 else { return }
    latest = Self.coarseLocation(location.coordinate)
  }

  /// desiredAccuracy is only a hint; do not retain a more precise fix if iOS supplies one.
  nonisolated static func coarseLocation(_ coordinate: CLLocationCoordinate2D) -> HomeLocation {
    HomeLocation(
      latitude: (coordinate.latitude * 100).rounded() / 100,
      longitude: (coordinate.longitude * 100).rounded() / 100
    )
  }

  func locationManager(_: CLLocationManager, didFailWithError _: Error) {
    // Keep the last truthful forecast; the next Home poll tries for another fix.
  }
}
