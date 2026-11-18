@preconcurrency import CoreLocation
import Observation

/// Keeps only the latest coarse fix in memory. Home sends it to the paired box when asking for
/// weather; it is not written to the public Home cache.
@MainActor
@Observable
final class HomeLocationSource: NSObject, CLLocationManagerDelegate {
  private(set) var latest: HomeLocation?

  @ObservationIgnored private let manager = CLLocationManager()

  override init() {
    super.init()
    manager.delegate = self
    manager.desiredAccuracy = kCLLocationAccuracyKilometer
  }

  func start() {
    switch manager.authorizationStatus {
    case .notDetermined:
      manager.requestWhenInUseAuthorization()
    case .authorizedAlways, .authorizedWhenInUse:
      manager.requestLocation()
    case .denied, .restricted:
      latest = nil
    @unknown default:
      latest = nil
    }
  }

  /// Refresh opportunistically for the next Home poll and return the last authorised fix now.
  func latestAndRefresh() -> HomeLocation? {
    if manager.authorizationStatus == .authorizedAlways
      || manager.authorizationStatus == .authorizedWhenInUse
    {
      manager.requestLocation()
    }
    return latest
  }

  func locationManagerDidChangeAuthorization(_: CLLocationManager) {
    start()
  }

  func locationManager(_: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
    guard let location = locations.last, location.horizontalAccuracy >= 0 else { return }
    latest = HomeLocation(
      latitude: location.coordinate.latitude,
      longitude: location.coordinate.longitude
    )
  }

  func locationManager(_: CLLocationManager, didFailWithError _: Error) {
    // Home keeps its last truthful forecast; the next minute poll asks for a fix again.
  }
}
