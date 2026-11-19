import Testing
@testable import LocalGhost

@Suite("Android static-view parity")
struct StaticContentTests {
  @Test("Glossary keeps Android's section and term order")
  func glossaryStructureMatchesAndroid() {
    #expect(GlossaryContent.sections.map(\.label) == [
      "HOW IT WORKS",
      "SYNC",
      "DAEMONS",
      "SECURITY",
    ])
    #expect(GlossaryContent.sections.map(\.terms.count) == [6, 5, 8, 7])
    #expect(GlossaryContent.sections.flatMap(\.terms).map(\.name) == [
      "The split",
      "Settings live on the box",
      "Chat: saved, incognito, web",
      "On-phone model (offline)",
      "Why split it",
      "What leaves the box",
      "Sync",
      "Ingest",
      "Index",
      "Per-device cursor",
      "Dedup",
      "Daemon",
      "ghost.framed",
      "ghost.voiced",
      "ghost.cued",
      "ghost.shadowd",
      "ghost.synthd",
      "ghost.watchd",
      "ghost.secd",
      "mTLS",
      "Code / PIN",
      "Code behaviours",
      "Codes are persona-scoped",
      "Persona / decoy",
      "Crypto-erase",
      "Change code vs reset",
    ])
  }

  @Test("Glossary defaults to the plain reading")
  func glossaryRegisterOrder() {
    #expect(GlossaryRegister.allCases == [.plain, .technical])
  }

  @Test("Static copy retains the privacy statements")
  func aboutCopyMatchesTheProductContract() {
    #expect(AboutContent.introduction.contains("thin client"))
    #expect(AboutContent.whatLeavesTheBox.contains("Your thoughts and words never leave your box"))
    #expect(GlossaryContent.sections.flatMap(\.terms).allSatisfy {
      !$0.plain.isEmpty && !$0.technical.isEmpty
    })
  }

  @Test("iOS copy uses the project's en-GB spelling")
  func copyUsesBritishEnglish() {
    let copy = GlossaryContent.sections.flatMap(\.terms).flatMap { [$0.plain, $0.technical] }
      + [AboutContent.introduction, AboutContent.whatLeavesTheBox]
    #expect(copy.contains { $0.contains("enrol") })
    #expect(!copy.contains { $0.localizedCaseInsensitiveContains("enroll") })
  }
}
