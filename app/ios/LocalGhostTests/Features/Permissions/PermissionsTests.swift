import AVFoundation
import CoreLocation
import Testing
@testable import LocalGhost

@MainActor
struct PermissionsTests {
  @Test
  func visitingAndRefreshingNeverRequestsAccess() {
    let recorder = PermissionRecorder()
    let model = recorder.model()
    model.refresh()
    for permission in AppPermission.allCases { _ = model.access(for: permission) }
    #expect(recorder.requests.isEmpty)
    #expect(recorder.settingsOpened == 0)
  }

  @Test(arguments: [AppPermission.camera, .location])
  func unaskedAccessIsRequestedOnlyAfterAnExplicitAction(_ permission: AppPermission) async {
    let recorder = PermissionRecorder()
    let model = recorder.model()
    await model.perform(permission)
    #expect(recorder.requests == [permission])
    #expect(recorder.settingsOpened == 0)
    #expect(model.access(for: permission) == .allowed)
    #expect(model.asking == nil)
  }

  @Test(arguments: [PermissionAccess.denied, .allowed, .approximate, .systemSettings])
  func existingDecisionsGoToSettingsWithoutRequestingAgain(_ access: PermissionAccess) async {
    let recorder = PermissionRecorder()
    recorder.access = access
    let model = recorder.model()
    await model.perform(.location)
    #expect(recorder.requests.isEmpty)
    #expect(recorder.settingsOpened == 1)
  }

  @Test(arguments: [PermissionAccess.restricted, .unavailable])
  func restrictedOrUnavailableAccessDoesNotOfferAnAction(_ access: PermissionAccess) async {
    let recorder = PermissionRecorder()
    recorder.access = access
    await recorder.model().perform(.camera)
    #expect(recorder.requests.isEmpty)
    #expect(recorder.settingsOpened == 0)
  }

  @Test
  func returningFromSystemSettingsReadsTheNewDecision() {
    let recorder = PermissionRecorder()
    let model = recorder.model()
    #expect(model.access(for: .camera) == .notAsked)
    recorder.access = .denied
    model.refresh()
    #expect(model.access(for: .camera) == .denied)
  }

  @Test
  func failedSettingsNavigationIsVisible() async {
    let recorder = PermissionRecorder()
    recorder.access = .denied
    recorder.canOpenSettings = false
    let model = recorder.model()
    await model.perform(.camera)
    #expect(model.errorMessage != nil)
    #expect(model.asking == nil)
  }

  @Test
  func preciseWeatherCoordinatesAreRoundedBeforeRetentionOrSending() {
    let coarse = HomeLocationSource.coarseLocation(
      CLLocationCoordinate2D(latitude: 12.345678, longitude: -45.678901))
    #expect(coarse == HomeLocation(latitude: 12.35, longitude: -45.68))
  }

  @Test
  func cameraMappingDoesNotConfuseRestrictionWithDenial() {
    #expect(PermissionAccess.camera(.notDetermined, available: true) == .notAsked)
    #expect(PermissionAccess.camera(.authorized, available: true) == .allowed)
    #expect(PermissionAccess.camera(.denied, available: true) == .denied)
    #expect(PermissionAccess.camera(.restricted, available: true) == .restricted)
    #expect(PermissionAccess.camera(.restricted, available: false) == .restricted)
    #expect(PermissionAccess.camera(.authorized, available: false) == .unavailable)
  }

  @Test
  func locationMappingPreservesApproximateAccessAndRestrictions() {
    #expect(PermissionAccess.location(.notDetermined, accuracy: .fullAccuracy) == .notAsked)
    #expect(PermissionAccess.location(.authorizedWhenInUse, accuracy: .reducedAccuracy) == .approximate)
    #expect(PermissionAccess.location(.authorizedWhenInUse, accuracy: .fullAccuracy) == .allowed)
    #expect(PermissionAccess.location(.authorizedAlways, accuracy: .reducedAccuracy) == .approximate)
    #expect(PermissionAccess.location(.denied, accuracy: .fullAccuracy) == .denied)
    #expect(PermissionAccess.location(.restricted, accuracy: .fullAccuracy) == .restricted)
  }
}

@MainActor
private final class PermissionRecorder {
  var access = PermissionAccess.notAsked
  var requests: [AppPermission] = []
  var settingsOpened = 0
  var canOpenSettings = true

  func model() -> PermissionsModel {
    PermissionsModel(client: PermissionClient(
      status: { _ in self.access },
      request: { permission in self.requests.append(permission); self.access = .allowed },
      openSettings: { self.settingsOpened += 1; return self.canOpenSettings }
    ))
  }
}
