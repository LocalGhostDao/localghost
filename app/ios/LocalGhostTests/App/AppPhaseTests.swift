import Testing

@testable import LocalGhost

struct AppPhaseTests {
  @Test
  func freshInstallStartsAtSetup() {
    #expect(AppPhase.initial == .setup)
  }

  @Test
  func navigationMatchesTheAndroidInformationArchitecture() {
    #expect(AppDestination.primary == [.home, .chat])
    #expect(AppDestination.archive == [.gallery, .map, .phrases, .health, .mail, .checkIn, .memories, .sync])
    #expect(AppDestination.box == [.integrations, .boxStatus, .models, .notifications, .connectors, .codes])
    #expect(AppDestination.application == [.settings, .permissions, .glossary, .about])
    #expect(Set(AppDestination.primary + AppDestination.archive + AppDestination.box + AppDestination.application).count == AppDestination.allCases.count)
  }
}
