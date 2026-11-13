import Testing

@testable import LocalGhost

struct AppPhaseTests {
  @Test
  func freshInstallStartsAtSetup() {
    #expect(AppPhase.initial == .setup)
  }
}
