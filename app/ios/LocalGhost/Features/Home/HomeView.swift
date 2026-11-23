import SwiftUI

nonisolated enum HomeLink: Sendable, Equatable {
  case crypto
  case coin(String)
  case weather
  case calendar
  case news
  case story(Int64)
  case memories
  case day(String)
  case memory(Int64)
}

@MainActor
struct HomeView: View {
  @Environment(GhostTheme.self) private var theme
  @State private var draft = ""
  @State private var briefRequest = 0
  @FocusState private var composerFocused: Bool

  let model: HomeModel
  let continuing: String
  let onAsk: (String) -> Bool
  let onNewChat: () -> Void
  let onOpenChat: () -> Void
  let onOpen: (HomeLink) -> Void

  init(
    model: HomeModel,
    continuing: String = "",
    onAsk: @escaping (String) -> Bool,
    onNewChat: @escaping () -> Void = {},
    onOpenChat: @escaping () -> Void = {},
    onOpen: @escaping (HomeLink) -> Void = { _ in }
  ) {
    self.model = model
    self.continuing = continuing
    self.onAsk = onAsk
    self.onNewChat = onNewChat
    self.onOpenChat = onOpenChat
    self.onOpen = onOpen
  }

  var body: some View {
    GhostScreen {
      ScrollView {
        LazyVStack(alignment: .leading, spacing: 12) {
          header
          pricesCard
          weatherCard
          if !upcomingEvents.isEmpty { calendarCard }
          newsCard
          if let forYou = model.forYou, !forYou.isEmpty {
            forYouCard(forYou)
          }
        }
        .padding(.horizontal, 20)
        .padding(.top, 16)
        .padding(.bottom, 12)
      }
      .refreshable { await model.refreshFromGesture() }
      .scrollDismissesKeyboard(.interactively)
      .safeAreaInset(edge: .bottom, spacing: 0) {
        VStack(spacing: 4) {
          if !continuing.isEmpty { continuingRow }
          composer
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 8)
        .background(theme.background)
        .overlay(alignment: .top) { Rectangle().fill(theme.border).frame(height: 1) }
      }
    }
    .task { await model.run() }
    .task(id: briefRequest) {
      guard briefRequest > 0 else { return }
      await model.writeBrief()
    }
  }

  private var header: some View {
    Text(headerText + (model.fetchingFeeds ? " · fetching the feeds…" : ""))
    .font(theme.font(.caption))
    .foregroundStyle(theme.textDim)
    .accessibilityIdentifier("home.date")
  }

  private var pricesCard: some View {
    VStack(alignment: .leading, spacing: 6) {
      let rows = priceRows
      if rows.isEmpty {
        statusLine(model.errorMessage == nil ? "reading the box's prices…" : "no prices: the box did not answer")
      } else {
        ForEach(rows) { row in
          Button { onOpen(.coin(row.symbol)) } label: {
            HStack(alignment: .lastTextBaseline, spacing: 8) {
              Text(row.symbol)
                .font(theme.font(.title3))
                .foregroundStyle(theme.accent)
                .frame(width: 56, alignment: .leading)
              HomeTickingPrice(
                text: money(row.price),
                value: row.price,
                colour: theme.text,
                riseColour: theme.accent,
                fallColour: theme.warning,
                font: theme.font(.title2)
              )
                .frame(maxWidth: .infinity, alignment: .leading)
              Text(change(row.change))
                .font(theme.font(.subheadline))
                .foregroundStyle((row.change ?? 0) < 0 ? theme.warning : theme.accent)
            }
            .contentShape(Rectangle())
          }
          .buttonStyle(.plain)
        }

        if let freshness = priceFreshness {
          Text(freshness)
            .font(theme.font(.caption2))
            .foregroundStyle(theme.accentDim)
        }
      }

      Button { onOpen(.crypto) } label: {
        HStack {
          Text(marketLine)
            .foregroundStyle(theme.textDim)
            .frame(maxWidth: .infinity, alignment: .leading)
          Text("prices ›").foregroundStyle(theme.accent)
        }
        .contentShape(Rectangle())
      }
      .buttonStyle(.plain)
      .font(theme.font(.caption))
    }
    .homeCard(theme: theme)
    .accessibilityIdentifier("home.prices")
  }

  private var weatherCard: some View {
    VStack(alignment: .leading, spacing: 6) {
      cardHeader("WEATHER") { onOpen(.weather) }
      if let weather = model.weather {
        weatherContents(weather)
      } else if !model.locationSupplied {
        statusLine("no position yet; allow location when you want weather for where you are")
      } else {
        statusLine(
          model.weatherErrorMessage.map { "the box has not answered with a forecast: \($0)" }
            ?? (model.weatherAttempted ? "the box has not answered with a forecast" : "reading the box's forecast…")
        )
      }
    }
    .homeCard(theme: theme)
    .contentShape(Rectangle())
    .onTapGesture { onOpen(.weather) }
    .accessibilityIdentifier("home.weather")
  }

  @ViewBuilder
  private func weatherContents(_ weather: HomeWeather) -> some View {
    if weather.noGeo == true {
      statusLine("nothing pulled: the box's place list is missing")
    } else if let forecast = weather.forecast, let place = forecast.place {
      let currentHour = currentWeatherHour(forecast)
      Text(currentHour.map(weatherNow) ?? forecast.now.map(weatherNow) ?? "")
        .font(theme.font(.headline))
        .foregroundStyle(theme.text)

      let hours = nextWeatherHours(forecast)
      if hours.count >= 6 {
        HomeWeatherLine(
          hours: hours,
          accent: theme.accent,
          text: theme.text,
          textDim: theme.textDim
        )
        .frame(height: 64)
      }

      if let days = weather.forecast?.days, !days.isEmpty {
        Text(days.prefix(3).map(weatherDay).joined(separator: "   "))
          .font(theme.font(.caption))
          .foregroundStyle(theme.textDim)
      }

      Text(weatherSource(weather, place: place))
        .font(theme.font(.caption2))
        .foregroundStyle(theme.accentDim)
    } else {
      statusLine(weather.text?.isEmpty == false ? weather.text! : "no forecast on the box yet")
    }
  }

  private var calendarCard: some View {
    VStack(alignment: .leading, spacing: 7) {
      cardHeader("NEXT") { onOpen(.calendar) }
      ForEach(upcomingEvents.prefix(3)) { event in
        Button { onOpen(.calendar) } label: {
          HStack(alignment: .top, spacing: 10) {
            Text(eventWhen(event))
              .font(theme.font(.caption))
              .foregroundStyle(theme.accent)
              .frame(width: 104, alignment: .leading)
            VStack(alignment: .leading, spacing: 2) {
              Text(event.title.isEmpty ? "(no title)" : event.title)
                .font(theme.font(.subheadline))
                .foregroundStyle(theme.text)
              if let location = event.location, !location.isEmpty {
                Text(location)
                  .font(theme.font(.caption))
                  .foregroundStyle(theme.textDim)
              }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
          }
          .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
      }
    }
    .homeCard(theme: theme)
    .accessibilityIdentifier("home.next")
  }

  private var newsCard: some View {
    VStack(alignment: .leading, spacing: 7) {
      HStack(spacing: 8) {
        Text("> THE DAY'S NEWS")
          .foregroundStyle(theme.accentDim)
        Spacer()
        Button(model.writingBrief ? "writing…" : "[ write now ]") {
          briefRequest += 1
        }
        .disabled(model.writingBrief)
        Button("all ›") { onOpen(.news) }
      }
      .font(theme.font(.caption))
      .buttonStyle(.plain)

      if let message = model.briefMessage {
        Text("! \(message)")
          .font(theme.font(.caption))
          .foregroundStyle(theme.warning)
      }

      if !briefPoints.isEmpty {
        ForEach(briefPoints) { point in
          Button { point.story.map { onOpen(.story($0)) } ?? onOpen(.news) } label: {
            HStack(alignment: .top, spacing: 7) {
              Text("•").foregroundStyle(theme.accent)
              Text(point.text)
                .foregroundStyle(theme.text)
                .frame(maxWidth: .infinity, alignment: .leading)
              if point.story != nil { Text("›").foregroundStyle(theme.accentDim) }
            }
            .font(theme.font(.callout))
            .contentShape(Rectangle())
          }
          .buttonStyle(.plain)
        }
        if effectiveBriefAt > 0 {
          Text("written \(age(from: effectiveBriefAt))")
            .font(theme.font(.caption2))
            .foregroundStyle(theme.textDim)
        }
      } else if newsRows.isEmpty {
        statusLine(
          model.errorMessage
            ?? (newsStoryCount == 0
              ? "no news on the box yet: pull down to fetch the feeds"
              : "the box writes the day's brief once a few stories have summaries")
        )
      } else {
        statusLine("the box writes the day's brief once a few stories have summaries")
        ForEach(newsRows.prefix(5)) { story in
          Button { onOpen(.story(story.id)) } label: {
            VStack(alignment: .leading, spacing: 2) {
              Text(story.title)
                .font(theme.font(.subheadline))
                .foregroundStyle(theme.text)
                .lineLimit(2)
              if !story.lead.isEmpty && story.lead != story.title {
                Text(story.lead)
                  .font(theme.font(.caption))
                  .foregroundStyle(theme.textDim)
                  .lineLimit(2)
              }
              Text(newsSourceLine(story))
                .font(theme.font(.caption2))
                .foregroundStyle(theme.accentDim)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
          }
          .buttonStyle(.plain)
        }
      }
    }
    .padding(.top, 8)
    .accessibilityIdentifier("home.news")
  }

  private func forYouCard(_ value: HomeForYou) -> some View {
    VStack(alignment: .leading, spacing: 7) {
      Text("> FOR YOU")
        .font(theme.font(.caption))
        .foregroundStyle(theme.accentDim)

      if !value.places.isEmpty {
        Text(nearbyLine(value.from))
          .font(theme.font(.caption2))
          .foregroundStyle(theme.accentDim)
        ForEach(value.places) { place in
          forYouRow(
            title: place.name + (place.isNew ? " · new" : ""),
            detail: [place.kind, distance(place.km), place.bearing].filter { !$0.isEmpty }.joined(separator: " · "),
            why: place.why,
            link: .memories
          )
        }
      }
      if !value.days.isEmpty {
        forYouHeading("this day")
      }
      ForEach(value.days) { day in
        forYouRow(
          title: "\(day.yearsAgo == 1 ? "1 year" : "\(day.yearsAgo) years") ago" + (day.title.isEmpty ? "" : " · \(day.title)"),
          detail: dayDetail(day).trimmingCharacters(in: CharacterSet(charactersIn: " ·")),
          why: day.lead,
          link: .day(day.day)
        )
      }
      if !value.stories.isEmpty {
        forYouHeading("from the news")
      }
      ForEach(value.stories) { story in
        forYouRow(title: story.title, detail: story.why, why: story.lead, link: .story(story.id))
      }
      if let memory = value.remember {
        forYouHeading(rememberHeading(memory.kind))
        forYouRow(title: memory.title, detail: "", why: memory.body, link: .memory(memory.id))
      }
    }
    .padding(.top, 8)
    .accessibilityIdentifier("home.forYou")
  }

  private func forYouRow(title: String, detail: String, why: String, link: HomeLink) -> some View {
    Button { onOpen(link) } label: {
      HStack(alignment: .top, spacing: 7) {
        Text("•").foregroundStyle(theme.accent)
        VStack(alignment: .leading, spacing: 2) {
          Text(title).font(theme.font(.subheadline)).foregroundStyle(theme.text)
          if !detail.isEmpty { Text(detail).foregroundStyle(theme.accentDim) }
          if !why.isEmpty { Text(why).foregroundStyle(theme.textDim) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        Text("›").foregroundStyle(theme.accentDim)
      }
      .font(theme.font(.caption))
      .frame(maxWidth: .infinity, alignment: .leading)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
  }

  private func forYouHeading(_ text: String) -> some View {
    Text(text)
      .font(theme.font(.caption2))
      .foregroundStyle(theme.accentDim)
      .padding(.top, 4)
  }

  private var continuingRow: some View {
    HStack(spacing: 8) {
      Button("continues “\(continuing.prefix(40))\(continuing.count > 40 ? "…" : "")” ›", action: onOpenChat)
        .lineLimit(1)
        .frame(maxWidth: .infinity, alignment: .leading)
      Button("[ new chat ]", action: onNewChat)
    }
    .buttonStyle(.plain)
    .font(theme.font(.caption2))
    .foregroundStyle(theme.accentDim)
  }

  private var composer: some View {
    HStack(alignment: .bottom, spacing: 8) {
      TextField("ask your box…", text: $draft, axis: .vertical)
        .font(theme.font(.callout))
        .foregroundStyle(theme.text)
        .tint(theme.accent)
        .lineLimit(1...4)
        .submitLabel(.send)
        .focused($composerFocused)
        .onSubmit(sendDraft)
        .padding(.horizontal, 6)
        .padding(.vertical, 10)

      Button(action: {}) {
        Image(systemName: "mic")
          .frame(width: 42, height: 42)
      }
      .disabled(true)
      .accessibilityLabel("Voice question")
      .accessibilityHint("Voice questions are not available on iOS yet")

      Button(action: sendDraft) {
        Text("›")
          .font(theme.font(.title2))
          .frame(width: 36, height: 36)
          .foregroundStyle(canSend ? theme.background : theme.textDim)
          .background(canSend ? theme.accent : theme.surface)
          .clipShape(Circle())
      }
      .disabled(!canSend)
      .accessibilityLabel("Ask the box")
    }
    .padding(.horizontal, 10)
    .padding(.vertical, 4)
    .background(theme.surface)
    .clipShape(RoundedRectangle(cornerRadius: 22))
    .overlay { RoundedRectangle(cornerRadius: 22).stroke(theme.border, lineWidth: 1) }
  }

  private var canSend: Bool { !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

  private func sendDraft() {
    let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty else { return }
    guard onAsk(text) else { return }
    draft = ""
    composerFocused = false
  }

  private func cardHeader(_ title: String, action: @escaping () -> Void) -> some View {
    HStack {
      Text(title).foregroundStyle(theme.accentDim)
      Spacer()
      Button("›", action: action).buttonStyle(.plain).foregroundStyle(theme.accentDim)
    }
    .font(theme.font(.caption))
  }

  private func statusLine(_ text: String) -> some View {
    Text(text)
      .font(theme.font(.caption))
      .foregroundStyle(theme.textDim)
      .frame(maxWidth: .infinity, alignment: .leading)
  }
}

private struct HomeTickingPrice: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.accessibilityReduceMotion) private var reduceMotion

  let text: String
  let value: Double
  let colour: Color
  let riseColour: Color
  let fallColour: Color
  let font: Font

  @State private var direction = 0
  @State private var pulse = false
  @State private var revision = 0

  var body: some View {
    HStack(alignment: .lastTextBaseline, spacing: 2) {
      Text(text)
        .font(font)
        .foregroundStyle(pulse ? movementColour : colour)
        .contentTransition(.numericText(value: value))
        .animation((reduceMotion || theme.preferences.reduceMotion) ? nil : .smooth(duration: 0.42), value: value)

      Text(direction >= 0 ? "▲" : "▼")
        .font(.system(size: 10, weight: .regular, design: .monospaced))
        .foregroundStyle(movementColour)
        .opacity(pulse && direction != 0 ? 1 : 0)
    }
    .onChange(of: value) { oldValue, newValue in
      guard oldValue > 0, newValue > 0, oldValue != newValue else { return }
      direction = newValue > oldValue ? 1 : -1
      revision &+= 1
      withAnimation((reduceMotion || theme.preferences.reduceMotion) ? nil : .easeOut(duration: 0.12)) { pulse = true }
    }
    .task(id: revision) {
      guard revision > 0 else { return }
      try? await Task.sleep(for: .seconds(2))
      guard !Task.isCancelled else { return }
      withAnimation((reduceMotion || theme.preferences.reduceMotion) ? nil : .easeOut(duration: 0.4)) { pulse = false }
    }
  }

  private var movementColour: Color {
    direction < 0 ? fallColour : riseColour
  }
}

private struct HomeWeatherLine: View {
  let hours: [HomeWeatherHour]
  let accent: Color
  let text: Color
  let textDim: Color

  var body: some View {
    Canvas { context, size in
      guard !hours.isEmpty else { return }

      let temperatures = hours.map(\.tempC)
      guard let low = temperatures.min(), let high = temperatures.max() else { return }
      let lowIndex = temperatures.firstIndex(of: low) ?? 0
      let highIndex = temperatures.firstIndex(of: high) ?? 0
      let top: CGFloat = 12
      let bottom = size.height - 16
      let rainHeight: CGFloat = 6
      let plotBottom = bottom - rainHeight - 2
      let step = hours.count > 1 ? size.width / CGFloat(hours.count - 1) : size.width

      func x(_ index: Int) -> CGFloat { CGFloat(index) * step }
      func y(_ temperature: Double) -> CGFloat {
        guard high - low >= 0.5 else { return top + (plotBottom - top) / 2 }
        return top + CGFloat((high - temperature) / (high - low)) * (plotBottom - top)
      }

      for (index, hour) in hours.enumerated() {
        let chance = max(0, min(100, hour.rainPct ?? 0))
        guard chance > 0 else { continue }
        let left = max(0, x(index) - step / 2 + 1)
        let width = min(size.width - left, max(1, step - 2))
        let rect = CGRect(x: left, y: plotBottom + 2, width: width, height: rainHeight)
        context.fill(
          Path(rect),
          with: .color(accent.opacity(0.15 + 0.6 * Double(chance) / 100))
        )
      }

      var temperaturePath = Path()
      for (index, hour) in hours.enumerated() {
        let point = CGPoint(x: x(index), y: y(hour.tempC))
        index == 0 ? temperaturePath.move(to: point) : temperaturePath.addLine(to: point)
      }
      context.stroke(temperaturePath, with: .color(accent), lineWidth: 2)

      for index in hours.indices where index.isMultiple(of: 6) {
        context.draw(
          Text(hourLabel(hours[index].at))
            .font(.system(size: 10, design: .monospaced))
            .foregroundColor(textDim),
          at: CGPoint(x: min(max(x(index), 8), size.width - 8), y: size.height - 3),
          anchor: .center
        )
      }

      drawTemperature(
        high,
        at: CGPoint(
          x: min(max(x(highIndex), 12), size.width - 12),
          y: max(9, y(high) - 5)
        ),
        context: &context
      )
      if lowIndex != highIndex {
        drawTemperature(
          low,
          at: CGPoint(
            x: min(max(x(lowIndex), 12), size.width - 12),
            y: min(plotBottom, y(low) + 12)
          ),
          context: &context
        )
      }
    }
    .accessibilityElement(children: .ignore)
    .accessibilityLabel("Hourly weather forecast")
    .accessibilityValue(accessibilitySummary)
  }

  private func drawTemperature(
    _ temperature: Double,
    at point: CGPoint,
    context: inout GraphicsContext
  ) {
    context.draw(
      Text("\(Int(temperature.rounded()))°")
        .font(.system(size: 10, design: .monospaced))
        .foregroundColor(text),
      at: point,
      anchor: .center
    )
  }

  private func hourLabel(_ timestamp: String) -> String {
    guard let separator = timestamp.firstIndex(of: "T") else {
      return String(timestamp.suffix(5).prefix(2))
    }
    return String(timestamp[timestamp.index(after: separator)...].prefix(2))
  }

  private var accessibilitySummary: String {
    guard let low = hours.map(\.tempC).min(), let high = hours.map(\.tempC).max() else { return "" }
    let wettest = hours.compactMap(\.rainPct).max() ?? 0
    return "High \(Int(high.rounded())) degrees, low \(Int(low.rounded())) degrees, rain up to \(wettest) percent"
  }
}

private nonisolated struct HomePriceRow: Identifiable {
  let symbol: String
  let price: Double
  let change: Double?
  let at: Int64
  let venues: Int
  var id: String { symbol }
}

private nonisolated struct HomeNewsRow: Identifiable {
  let id: Int64
  let title: String
  let lead: String
  let outlets: [String]
  let sources: Int
  let lastSeen: Int64

}

private nonisolated struct HomeBriefPoint: Identifiable {
  let offset: Int
  let text: String
  let story: Int64?
  var id: Int { offset }
}

@MainActor
private extension HomeView {
  var priceRows: [HomePriceRow] {
    ["BTC", "ETH"].compactMap { symbol in
      let candidates: [HomePriceRow] = [
        model.fastRates?.index[symbol].flatMap { value in
          value.price > 0 ? HomePriceRow(symbol: symbol, price: value.price, change: value.change, at: value.at, venues: value.n) : nil
        },
        model.rates?.index[symbol].flatMap { value in
          value.price > 0 ? HomePriceRow(symbol: symbol, price: value.price, change: value.change, at: value.at, venues: value.n) : nil
        },
        model.snapshot?.prices[symbol].flatMap { value in
          value.price > 0 ? HomePriceRow(symbol: symbol, price: value.price, change: value.change, at: value.at, venues: value.n) : nil
        },
      ].compactMap { $0 }
      return candidates.max { normalisedSeconds($0.at) < normalisedSeconds($1.at) }
    }
  }

  var headerText: String {
    let locale = Locale(identifier: "en_GB")
    let day = model.now.formatted(
      .dateTime.weekday(.wide).day().month(.wide).locale(locale)
    ).replacingOccurrences(of: ",", with: "")
    let time = model.now.formatted(
      .dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).locale(locale)
    )
    return "\(day) · \(time)"
  }

  var priceFreshness: String? {
    guard let newest = priceRows.max(by: { $0.at < $1.at }) else { return nil }
    let seconds = newest.at > 10_000_000_000 ? newest.at / 1_000 : newest.at
    let venues = newest.venues > 1 ? " · \(newest.venues) exchanges" : ""
    return "updated \(age(from: seconds))\(venues)"
  }

  var marketLine: String {
    let ratesAt = model.rates?.index.values.map { normalisedSeconds($0.at) }.max() ?? 0
    if let snapshot = model.snapshot,
       snapshot.at >= ratesAt,
       let value = snapshot.marketValue,
       value > 0
    {
      return "\(snapshot.marketCode ?? "CRYPTO50") \(String(format: "%.1f", locale: Locale(identifier: "en_GB"), value))  \(change(snapshot.marketChange)) today"
    }
    if let market = model.rates?.market, market.value > 0 {
      return "\(market.code) \(String(format: "%.1f", locale: Locale(identifier: "en_GB"), market.value))  \(change(market.dayChange)) today"
    }
    if let value = model.snapshot?.marketValue, value > 0 {
      return "\(model.snapshot?.marketCode ?? "CRYPTO50") \(String(format: "%.1f", locale: Locale(identifier: "en_GB"), value))  \(change(model.snapshot?.marketChange)) today"
    }
    return ""
  }

  func normalisedSeconds(_ timestamp: Int64) -> Int64 {
    timestamp > 10_000_000_000 ? timestamp / 1_000 : timestamp
  }

  var upcomingEvents: [HomeCalendarEvent] {
    let now = Int64(model.now.timeIntervalSince1970)
    let end = now + 36 * 3_600
    return (model.snapshot?.calendar ?? [])
      .filter { $0.isLive && $0.allDay != true && $0.end > now && $0.begin < end }
      .sorted { $0.begin < $1.begin }
  }

  var effectiveBrief: String {
    let newsAt = model.news?.briefAt ?? 0
    let snapAt = model.snapshot?.briefAt ?? 0
    return snapAt > newsAt ? (model.snapshot?.brief ?? "") : (model.news?.brief ?? "")
  }

  var effectiveBriefAt: Int64 {
    max(model.news?.briefAt ?? 0, model.snapshot?.briefAt ?? 0)
  }

  var effectiveBriefStories: [Int64] {
    let newsAt = model.news?.briefAt ?? 0
    let snapAt = model.snapshot?.briefAt ?? 0
    return snapAt > newsAt ? (model.snapshot?.briefStories ?? []) : (model.news?.briefStories ?? [])
  }

  var briefPoints: [HomeBriefPoint] {
    let raw = effectiveBrief.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !raw.isEmpty else { return [] }
    let bulletLines = raw.components(separatedBy: .newlines).compactMap { line -> String? in
      let line = line.trimmingCharacters(in: .whitespacesAndNewlines)
      guard line.hasPrefix("- ") || line.hasPrefix("• ") else { return nil }
      return String(line.dropFirst(2)).trimmingCharacters(in: .whitespaces)
    }.filter { !$0.isEmpty }
    let texts = bulletLines.isEmpty ? [raw] : bulletLines
    let paired = bulletLines.count == effectiveBriefStories.count
    return texts.enumerated().map {
      HomeBriefPoint(offset: $0.offset, text: $0.element, story: paired ? effectiveBriefStories[$0.offset] : nil)
    }
  }

  var newsRows: [HomeNewsRow] {
    let now = Int64(model.now.timeIntervalSince1970)
    if let stories = model.news?.stories, !stories.isEmpty {
      return stories.filter {
        !$0.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
          && now - $0.lastSeen <= 86_400
      }.sorted {
        $0.sources == $1.sources ? $0.lastSeen > $1.lastSeen : $0.sources > $1.sources
      }.map {
        HomeNewsRow(
          id: $0.id,
          title: $0.title,
          lead: $0.lead,
          outlets: $0.items.map(\.outlet),
          sources: $0.sources,
          lastSeen: $0.lastSeen
        )
      }
    }
    return (model.snapshot?.top ?? []).filter {
      !$0.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        && now - $0.lastSeen <= 86_400
    }.map {
      HomeNewsRow(id: $0.id, title: $0.title, lead: $0.lead, outlets: $0.outlets, sources: $0.sources, lastSeen: $0.lastSeen)
    }
  }

  var newsStoryCount: Int {
    model.news?.stories.count ?? model.snapshot?.top.count ?? 0
  }

  func age(from seconds: Int64) -> String {
    let age = max(0, Int64(model.now.timeIntervalSince1970) - seconds)
    return switch age {
    case 0..<2: "just now"
    case 2..<90: "\(age) s ago"
    case 90..<(90 * 60): "\((age + 30) / 60) min ago"
    case (90 * 60)..<(48 * 3_600): "\((age + 1_800) / 3_600) h ago"
    default: "\(age / 86_400) days ago"
    }
  }

  func money(_ value: Double) -> String {
    switch value {
    case ...0: "0"
    case ..<1: String(format: "%.4f", locale: Locale(identifier: "en_GB"), value)
    case ..<1_000: String(format: "%.2f", locale: Locale(identifier: "en_GB"), value)
    default: value.formatted(.number.precision(.fractionLength(0)).locale(Locale(identifier: "en_GB")))
    }
  }

  func change(_ value: Double?) -> String {
    guard let value else { return "" }
    return String(format: "%+.1f%%", locale: Locale(identifier: "en_GB"), value)
  }

  func eventWhen(_ event: HomeCalendarEvent) -> String {
    let now = Int64(model.now.timeIntervalSince1970)
    if event.begin <= now, event.end > now {
      return "now, until \(time(event.end))"
    }
    let delta = event.begin - now
    if delta >= 0, delta < 3_600 { return "in \(max(1, (delta + 59) / 60)) min" }
    if Calendar.current.isDate(Date(timeIntervalSince1970: TimeInterval(event.begin)), inSameDayAs: model.now) {
      return "at \(time(event.begin))"
    }
    let date = Date(timeIntervalSince1970: TimeInterval(event.begin))
    if Calendar.current.isDateInTomorrow(date) { return "tomorrow \(time(event.begin))" }
    return date.formatted(
      .dateTime
        .weekday(.abbreviated)
        .day()
        .month(.abbreviated)
        .hour(.twoDigits(amPM: .omitted))
        .minute(.twoDigits)
        .locale(Locale(identifier: "en_GB"))
    )
  }

  func time(_ seconds: Int64) -> String {
    Date(timeIntervalSince1970: TimeInterval(seconds)).formatted(
      .dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).locale(Locale(identifier: "en_GB"))
    )
  }

  func weatherNow(_ current: HomeWeather.Forecast.Now) -> String {
    var pieces = ["\(Int(current.tempC.rounded()))°"]
    if let feels = current.feelsC, abs(feels - current.tempC) >= 1.5 {
      pieces[0] += " feels \(Int(feels.rounded()))°"
    }
    let word = weatherWord(current.code)
    if !word.isEmpty { pieces.append(word) }
    if current.windKmh >= 20 { pieces.append("wind \(Int(current.windKmh.rounded())) km/h") }
    return pieces.joined(separator: " · ")
  }

  func weatherNow(_ current: HomeWeatherHour) -> String {
    var pieces = ["\(Int(current.tempC.rounded()))°"]
    let word = weatherWord(current.code)
    if !word.isEmpty { pieces.append(word) }
    if current.windKmh >= 20 { pieces.append("wind \(Int(current.windKmh.rounded())) km/h") }
    return pieces.joined(separator: " · ")
  }

  func weatherDay(_ day: HomeWeatherDay) -> String {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_GB")
    formatter.calendar = Calendar(identifier: .gregorian)
    formatter.dateFormat = "yyyy-MM-dd"
    let weekday: String
    if let date = formatter.date(from: day.date) {
      formatter.dateFormat = "EEE"
      weekday = formatter.string(from: date)
    } else {
      weekday = String(day.date.suffix(2))
    }
    let word = weatherWord(day.code)
    let rain = day.rainPct >= 30 ? " \(day.rainPct)%" : ""
    return "\(weekday) \(Int(day.maxC.rounded()))/\(Int(day.minC.rounded()))\(word.isEmpty ? "" : " \(word)")\(rain)"
  }

  func weatherWord(_ code: Int) -> String {
    switch code {
    case 0: "clear"
    case 1: "mostly clear"
    case 2: "partly cloudy"
    case 3: "overcast"
    case 45, 48: "fog"
    case 51, 53, 55: "drizzle"
    case 56, 57: "freezing drizzle"
    case 61, 63, 65: "rain"
    case 66, 67: "freezing rain"
    case 71, 73, 75, 77: "snow"
    case 80, 81, 82: "showers"
    case 85, 86: "snow showers"
    case 95, 96, 99: "thunderstorm"
    default: ""
    }
  }

  func weatherSource(_ weather: HomeWeather, place: HomeWeather.Forecast.Place) -> String {
    let whereText = weather.forecast?.here == true ? "where you are, near \(place.name)" : [place.name, place.country].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: ", ")
    let fetched = weather.forecast?.fetchedAt ?? weather.table?.fetchedAt ?? 0
    let source = weather.forecast?.source ?? ""
    if !source.isEmpty {
      let runs = source.split(separator: "·").compactMap { run -> String? in
        let parts = run.split(whereSeparator: \.isWhitespace)
        guard parts.count >= 3, let hour = parts.last else { return nil }
        return "\(parts[0].uppercased()) \(hour)"
      }.joined(separator: ", ")
      return "\(whereText) · the box's own forecast\(runs.isEmpty ? "" : " from \(runs)") · computed \(age(from: fetched))"
    }
    let places = weather.table?.places.map { " · nearest of \($0) places" } ?? ""
    return "\(whereText)\(places) · pulled \(age(from: fetched))"
  }

  func nearbyLine(_ from: Int64) -> String {
    guard from > 0 else { return "near you" }
    return "near you · from your trail \(age(from: from))"
  }

  func distance(_ km: Double) -> String {
    if km < 1 { return "\(Int((km * 20).rounded()) * 50) m" }
    if km < 10 { return String(format: "%.1f km", locale: Locale(identifier: "en_GB"), km) }
    return "\(Int(km.rounded())) km"
  }

  func dayDetail(_ day: HomeForYouDay) -> String {
    var parts: [String] = []
    if day.photos > 0 { parts.append(day.photos == 1 ? "1 photo" : "\(day.photos) photos") }
    if let place = day.place, !place.isEmpty { parts.append(place) }
    return parts.isEmpty ? "" : " · " + parts.joined(separator: " · ")
  }

  func rememberHeading(_ kind: String) -> String {
    switch kind {
    case "insight": "your box noticed"
    case "place": "a place of yours"
    case "person": "one of your people"
    default: "remembered"
    }
  }

  func newsSourceLine(_ story: HomeNewsRow) -> String {
    var distinct: [String] = []
    for outlet in story.outlets where !outlet.isEmpty && !distinct.contains(outlet) {
      distinct.append(outlet)
    }
    let shown = distinct.count <= 3
      ? distinct
      : Array(distinct.prefix(3)) + ["\(distinct.count - 3) more"]
    let names = shown.isEmpty
      ? ["\(story.sources) source\(story.sources == 1 ? "" : "s")"]
      : shown
    return (names + [age(from: story.lastSeen)]).joined(separator: " · ")
  }

  func currentWeatherHour(_ forecast: HomeWeather.Forecast) -> HomeWeatherHour? {
    let now = Int64(model.now.timeIntervalSince1970)
    if let fetched = forecast.fetchedAt, fetched > 0, now - fetched < 90 * 60 { return nil }
    let key = weatherHourKey(now: model.now, offset: forecast.utcOffset ?? 0)
    return forecast.hours?.first { $0.at == key }
  }

  func nextWeatherHours(_ forecast: HomeWeather.Forecast) -> [HomeWeatherHour] {
    let hours = forecast.hours ?? []
    let key = weatherHourKey(now: model.now, offset: forecast.utcOffset ?? 0)
    guard let index = hours.firstIndex(where: { $0.at == key }) else { return [] }
    return Array(hours[index..<min(hours.count, index + 24)])
  }

  func weatherHourKey(now: Date, offset: Int) -> String {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.calendar = Calendar(identifier: .gregorian)
    formatter.timeZone = TimeZone(secondsFromGMT: min(max(offset, -18 * 3_600), 18 * 3_600))
    formatter.dateFormat = "yyyy-MM-dd'T'HH:00"
    return formatter.string(from: now)
  }
}

private extension View {
  func homeCard(theme: GhostTheme) -> some View {
    self
      .padding(14)
      .frame(maxWidth: .infinity, alignment: .leading)
      .background(theme.surface)
      .overlay { Rectangle().stroke(theme.border, lineWidth: 1) }
  }
}
