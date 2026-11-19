import SwiftUI

enum GlossaryRegister: String, CaseIterable, Identifiable {
  case plain
  case technical

  var id: Self { self }
}

struct GlossaryTerm: Identifiable, Equatable {
  let name: String
  let technical: String
  let plain: String

  var id: String { name }
}

struct GlossarySection: Identifiable, Equatable {
  let label: String
  let terms: [GlossaryTerm]

  var id: String { label }
}

struct GlossaryView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var register: GlossaryRegister = .plain

  var body: some View {
    GhostScreen {
      ScrollView {
        LazyVStack(alignment: .leading, spacing: 14) {
          TerminalSectionLabel(text: "GLOSSARY")

          Text("How everything works, and what every word means. Two readings, pick yours.")
            .font(theme.font(.body))
            .foregroundStyle(theme.textDim)

          HStack(spacing: 12) {
            ForEach(GlossaryRegister.allCases) { choice in
              registerButton(choice)
            }
          }
          .padding(.bottom, 4)

          ForEach(GlossaryContent.sections) { section in
            TerminalSectionLabel(text: section.label)
              .padding(.top, 6)

            ForEach(section.terms) { term in
              GlossaryTermCard(term: term, register: register)
            }
          }

          Color.clear.frame(height: 24)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 20)
        .padding(.top, 12)
      }
      .scrollIndicators(.hidden)
    }
    .accessibilityIdentifier("glossary.screen")
  }

  private func registerButton(_ choice: GlossaryRegister) -> some View {
    let selected = register == choice

    return Button { register = choice } label: {
      Text("[ \(choice.rawValue.uppercased()) ]")
        .font(theme.font(.caption))
        .foregroundStyle(selected ? theme.background : theme.textDim)
        .padding(.horizontal, 12)
        .frame(minHeight: 42)
        .background(selected ? theme.accent : theme.background)
        .overlay {
          Rectangle().stroke(selected ? theme.accent : theme.border, lineWidth: 1)
        }
        .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .accessibilityLabel("\(choice.rawValue.capitalized) glossary")
    .accessibilityAddTraits(selected ? .isSelected : [])
    .accessibilityIdentifier("glossary.\(choice.rawValue)")
  }
}

private struct GlossaryTermCard: View {
  @Environment(GhostTheme.self) private var theme

  let term: GlossaryTerm
  let register: GlossaryRegister

  var body: some View {
    VStack(alignment: .leading, spacing: 6) {
      Text(term.name)
        .font(theme.font(.headline, weight: .medium))
        .foregroundStyle(theme.accent)

      Text(register == .technical ? term.technical : term.plain)
        .font(theme.font(.body))
        .foregroundStyle(theme.text)
        .fixedSize(horizontal: false, vertical: true)
    }
    .frame(maxWidth: .infinity, alignment: .leading)
    .padding(14)
    .background(theme.surface)
    .overlay {
      Rectangle().stroke(theme.border, lineWidth: 1)
    }
    .accessibilityElement(children: .combine)
  }
}

enum GlossaryContent {
  static let sections: [GlossarySection] = [
    GlossarySection(label: "HOW IT WORKS", terms: [
      GlossaryTerm(
        name: "The split",
        technical: "Two machines. The phone is a stateless client. The box (your hardware) holds the model, the index, and all extracted data. The phone authenticates over mTLS, streams raw media up, and renders results. It persists no index, no model, no memories, no sync cursor.",
        plain: "There are two computers: your phone and a small box you own at home. The box does all the thinking and keeps everything. Your phone is just a window into it. If you lose the phone, your data is still safe at home on the box."
      ),
      GlossaryTerm(
        name: "Settings live on the box",
        technical: "Settings, codes, and persona state are box-owned and persona-scoped. The phone reads them from ghost.secd and writes changes back; it caches only for offline display. Every enroled device therefore sees the same state.",
        plain: "Your settings aren't stored on the phone. They live on the box, so all your devices agree, and a new phone just picks up where you left off."
      ),
      GlossaryTerm(
        name: "Chat: saved, incognito, web",
        technical: "The two icons in the chat's top bar (the hat and glasses, and the globe). ‘saved’: the conversation is stored on the box (questions, answers, the model's thinking and the web sources it used), and an answer keeps being written there if the app closes. ‘incognito’: nothing is stored. Web: ‘web off’ answers from your archive alone; ‘web auto’ (the start) searches on the phone when a question needs the outside world (news, prices, weather, who is, how much); ‘web on’ searches for every question. The phone searches (DuckDuckGo, or Brave with your key) and hands the findings to the box; the box never searches the web for you. It does pull general information for context (the news feeds and the market tickers it follows, a plugin's data) when the phone is not on Wi-Fi to fetch it; your words never go out.",
        plain: "Tap the hat to switch to incognito, where the chat is not kept anywhere. Tap the globe to choose whether your phone looks things up online for the box: never, when it seems needed, or always. Closing the app no longer loses an answer: the box finishes it and it is there when you come back."
      ),
      GlossaryTerm(
        name: "On-phone model (offline)",
        technical: "A small model your box serves to the phone, run locally via llama.cpp. Used only when the box is unreachable, or when you force local mode. It has NO access to your life-index (that's on the box), so it answers generic questions with limited context. The box's full-context RAG stays primary.",
        plain: "Your box can hand the phone a small AI to keep on it. If the box goes offline, the phone uses that for general questions. It can't see your life (that stays on the box), so it's a backup, not the real thing. When the box is back, it takes over again."
      ),
      GlossaryTerm(
        name: "Why split it",
        technical: "Privacy through architecture, not policy. The phone can be seized, lost, or compromised; it holds nothing of value. The box is yours, in your home, behind your own keys. Nobody (including us) is in the loop.",
        plain: "Phones get lost and stolen. So nothing important is kept on the phone. Everything lives on the box at home, where only you can reach it. No company can see it, because no company is involved."
      ),
      GlossaryTerm(
        name: "What leaves the box",
        technical: "Outbound traffic is pull-only and content-free: the box fetches the news feeds and market tickers in its sources list, and what a connected plugin asks for, through one client (internal/egress) with a browser-like agent and no cookies, and only when the phone has not said it is on Wi-Fi in the last twenty minutes (then the phone fetches and hands the bytes over). Nothing from your archive is in a request.",
        plain: "Your thoughts and words never leave the box. It does pull general things from outside to use in context, the news and the prices it follows, so a publisher or an exchange may see your box's address the way a website sees a browser. It only pulls; it never sends anything of yours."
      ),
    ]),
    GlossarySection(label: "SYNC", terms: [
      GlossaryTerm(
        name: "Sync",
        technical: "The phone copies new camera-roll photos and videos to the box. Originals stay on the phone; the box receives copies for extraction. Runs every 15 minutes over Wi-Fi (configurable to mobile) and when you open the app. A cursor on the box tracks position, so only new items move.",
        plain: "Your phone sends copies of new photos and videos to the box so it can understand them. Your originals stay on your phone. It happens quietly every 15 minutes on Wi-Fi, and when you open the app."
      ),
      GlossaryTerm(
        name: "Ingest",
        technical: "The act of the box receiving and storing a synced item before extraction. Raw bytes in; structured memory out.",
        plain: "When the box takes in a photo or video you sent and files it away to look at."
      ),
      GlossaryTerm(
        name: "Index",
        technical: "The searchable store the daemons build from your media. What retrieval runs against. Lives on the box only.",
        plain: "A kind of organised memory the box builds of your life, so it can answer questions about it. Kept on the box."
      ),
      GlossaryTerm(
        name: "Per-device cursor",
        technical: "Sync position is tracked per (persona, device, stream). Each phone advances its own cursor; they do not interfere. Enrol two phones and each syncs independently.",
        plain: "If you use more than one phone, the box remembers separately how far each one has sent, so they don't trip over each other."
      ),
      GlossaryTerm(
        name: "Dedup",
        technical: "Content-addressed dedup. Each ingested item is hashed; a hash already present is linked, not re-extracted. The same photo from two devices is one memory, attributed to both.",
        plain: "If two of your phones have the same photo, the box keeps it once, not twice, but remembers it came from both."
      ),
    ]),
    GlossarySection(label: "DAEMONS", terms: [
      GlossaryTerm(
        name: "Daemon",
        technical: "A background process on the box. A daemon does one job and stays out of the way. It is without surveillance. Each has one job and runs continuously.",
        plain: "A small program on the box that runs on its own and does one job, quietly, all the time. Like a helper that never sleeps and never reports to anyone."
      ),
      GlossaryTerm(
        name: "ghost.framed",
        technical: "Extracts moments from photos and video. Reads EXIF, decodes frames, produces structured journal entries.",
        plain: "Looks at your photos and videos and writes down what happened in them."
      ),
      GlossaryTerm(
        name: "ghost.voiced",
        technical: "Captures and transcribes voice notes.",
        plain: "Records and writes out your voice notes."
      ),
      GlossaryTerm(
        name: "ghost.cued",
        technical: "Surfaces reflections (patterns worth your attention drawn from the index).",
        plain: "Notices patterns in your life and points them out when useful."
      ),
      GlossaryTerm(
        name: "ghost.shadowd",
        technical: "Scans messages for manipulation patterns and flags them.",
        plain: "Watches for messages trying to manipulate you and warns you."
      ),
      GlossaryTerm(
        name: "ghost.synthd",
        technical: "The chat brain. Retrieves relevant memories, injects them as context, and runs the local model to answer. RAG over your life.",
        plain: "The part you chat with. It looks up the right memories and answers using them."
      ),
      GlossaryTerm(
        name: "ghost.watchd",
        technical: "Watches the other daemons. Health, liveness, honesty.",
        plain: "Keeps an eye on the other helpers to make sure they're working."
      ),
      GlossaryTerm(
        name: "ghost.secd",
        technical: "The keystone. Authentication, enrolment, persona custody, key management. Nothing else runs until it does.",
        plain: "The guard at the door. Handles your code, proves the phone is yours, and protects the keys."
      ),
    ]),
    GlossarySection(label: "SECURITY", terms: [
      GlossaryTerm(
        name: "mTLS",
        technical: "Mutual TLS. Both ends prove identity with certificates: the box proves it's your box, the phone proves it's your enroled phone. No shared passwords on the wire.",
        plain: "A two-way handshake so the phone and box each prove who they are before talking. Stops anyone pretending to be either one."
      ),
      GlossaryTerm(
        name: "Code / PIN",
        technical: "The code is not authentication (the certificate is). It selects which persona the box mounts. It derives a key; the box tries to open a volume with it. Right key opens the real persona; a decoy key opens a decoy.",
        plain: "Your code doesn't unlock the app so much as choose which set of data the box opens. Different codes can open different things."
      ),
      GlossaryTerm(
        name: "Code behaviours",
        technical: "Each code carries a behaviour: MOUNT REAL opens the persona it belongs to, MOUNT DECOY opens a fallback persona, WIPE is a GLOBAL panic erase that destroys the master key-encrypting key so every persona's volume becomes noise at once, from any persona. ‘Real’ is relative to the persona you are in. There is no absolute flag.",
        plain: "Each code does something: open your space, open a fake space, or wipe everything. What counts as ‘yours’ depends on which code you used. Decoy and wipe codes can be changed but never deleted. A wipe code is global: entered from any persona it erases everything, every persona at once."
      ),
      GlossaryTerm(
        name: "Codes are persona-scoped",
        technical: "The code list you can see and manage belongs only to the mounted persona. The box cannot enumerate another persona's codes while this one is open. It lacks the keys. No view aggregates across personas.",
        plain: "You can only see the codes for the space you're currently in. The box genuinely can't show codes from another space, and nobody can prove other spaces exist."
      ),
      GlossaryTerm(
        name: "Persona / decoy",
        technical: "Separate encrypted volumes on the box. A duress or decoy code mounts a plausible alternative while your real data stays sealed. The number of personas is unprovable.",
        plain: "You can have a real space and a fake one. Under pressure, a different code opens the fake, and nobody can tell the real one exists."
      ),
      GlossaryTerm(
        name: "Crypto-erase",
        technical: "Wiping by destroying the key, not the bytes. Without the wrapping key the data is noise. Instant, irreversible, complete.",
        plain: "To erase, the box throws away the key. The data is still there but unreadable forever (like shredding the only translation of a locked book)."
      ),
      GlossaryTerm(
        name: "Change code vs reset",
        technical: "changepin re-wraps the slot's key under a new code, with the current code as the second factor: the data carries forward. resetup is for a lost code: it destroys the slot's key and starts the slot afresh; the old data is unreadable forever. Both run at the box over a local-network session only, never from a phone.",
        plain: "Changing your code keeps your data; you need the old code to do it. Resetting is for a lost code and wipes that slot. Neither can be done from the app, which is on purpose: a phone taken from you cannot change or reset a code."
      ),
    ]),
  ]
}

#Preview {
  GlossaryView()
    .environment(GhostTheme())
    .preferredColorScheme(.dark)
}
