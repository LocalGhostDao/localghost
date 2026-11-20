import SwiftUI

struct AboutView: View {
  @Environment(GhostTheme.self) private var theme

  var body: some View {
    GhostScreen {
      ScrollView {
        VStack(alignment: .leading, spacing: 0) {
          TerminalSectionLabel(text: "ABOUT")
            .padding(.bottom, 16)

          Text("LOCALGHOST")
            .font(theme.font(.title2))
            .foregroundStyle(theme.accent)

          Text("> the only cloud is you")
            .font(theme.font(.body))
            .foregroundStyle(theme.textDim)
            .padding(.top, 8)

          Text(AboutContent.introduction)
            .aboutBody(theme: theme)
            .padding(.top, 24)

          TerminalSectionLabel(text: "WHAT LEAVES THE BOX")
            .padding(.top, 24)
            .padding(.bottom, 8)

          Text(AboutContent.whatLeavesTheBox)
            .aboutBody(theme: theme)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(20)
        .padding(.bottom, 24)
      }
      .scrollIndicators(.hidden)
    }
    .accessibilityIdentifier("about.screen")
  }
}

enum AboutContent {
  static let introduction =
  "All processing and storage runs on hardware you own. " +
  "This app is a thin client: it authenticates over mTLS, syncs media, and reads results. " +
  "It holds no index and no model. If you lose the phone, the data is on the box, not in your pocket."

  static let whatLeavesTheBox =
  "Your thoughts and words never leave your box: your photos, notes, trail, health, chats and memories " +
  "are read and written on it alone, and nothing of them is sent anywhere. The box does pull general " +
  "information from outside to use in context: the news feeds and the market tickers it follows, the " +
  "articles behind the stories it summarises, and what a plugin you connect asks for. When your phone " +
  "is on Wi-Fi the phone fetches the feeds for it; the prices and the articles the box fetches itself, " +
  "so a publisher or an exchange may see your box's address, the same as a browser's. It pulls only; " +
  "it tells them nothing. An article is read for its story's summary and then let go: the box keeps the " +
  "summary, not the article."

}

private extension View {
  func aboutBody(theme: GhostTheme) -> some View {
    font(theme.font(.body))
      .foregroundStyle(theme.text)
      .fixedSize(horizontal: false, vertical: true)
  }
}

#Preview {
  AboutView()
    .environment(GhostTheme())
    .preferredColorScheme(.dark)
}
