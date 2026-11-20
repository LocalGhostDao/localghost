import Foundation
import Network
import Darwin

nonisolated struct HomeLocation: Sendable, Equatable {
  let latitude: Double
  let longitude: Double
}

nonisolated struct HomePrice: Codable, Sendable, Equatable {
  let price: Double
  let change24: Double
  let hasChange: Bool
  let at: Int64
  let n: Int

  var change: Double? { hasChange ? change24 : nil }
}

nonisolated struct HomeStory: Codable, Sendable, Equatable, Identifiable {
  let id: Int64
  let title: String
  let lead: String
  let outlets: [String]
  let sources: Int
  let lastSeen: Int64

  private enum CodingKeys: String, CodingKey {
    case id, title, lead, outlets, sources, lastSeen
  }

  init(from decoder: Decoder) throws {
    let values = try decoder.container(keyedBy: CodingKeys.self)
    id = try values.decode(Int64.self, forKey: .id)
    title = try values.decode(String.self, forKey: .title)
    lead = try values.decode(String.self, forKey: .lead)
    outlets = try values.decodeIfPresent([String].self, forKey: .outlets) ?? []
    sources = try values.decode(Int.self, forKey: .sources)
    lastSeen = try values.decode(Int64.self, forKey: .lastSeen)
  }

  init(id: Int64, title: String, lead: String, outlets: [String], sources: Int, lastSeen: Int64) {
    self.id = id
    self.title = title
    self.lead = lead
    self.outlets = outlets
    self.sources = sources
    self.lastSeen = lastSeen
  }
}

nonisolated struct HomeForYouPlace: Codable, Sendable, Equatable, Identifiable {
  let name: String
  let kind: String
  let km: Double
  let bearing: String
  let why: String
  let isNew: Bool

  var id: String { "\(name)|\(kind)|\(km)" }

  private enum CodingKeys: String, CodingKey {
    case name, kind, km, bearing, why
    case isNew = "new"
  }
}

nonisolated struct HomeForYouDay: Codable, Sendable, Equatable, Identifiable {
  let day: String
  let yearsAgo: Int
  /// Nil for photo-only anniversaries; the server omits this field in that case.
  let memoryId: Int64?
  let title: String
  let lead: String
  let photos: Int
  let place: String?

  var id: String { day }
}

nonisolated struct HomeForYouStory: Codable, Sendable, Equatable, Identifiable {
  let id: Int64
  let title: String
  let lead: String
  let why: String
}

nonisolated struct HomeForYouMemory: Codable, Sendable, Equatable, Identifiable {
  let id: Int64
  let kind: String
  let title: String
  let body: String
}

nonisolated struct HomeForYou: Codable, Sendable, Equatable {
  let at: Int64
  let from: Int64
  let places: [HomeForYouPlace]
  let days: [HomeForYouDay]
  let stories: [HomeForYouStory]
  let remember: HomeForYouMemory?
  let note: String?

  var isEmpty: Bool {
    places.isEmpty && days.isEmpty && stories.isEmpty && remember == nil
  }
}

nonisolated struct HomeCalendarAttendee: Codable, Sendable, Equatable {
  let name: String?
  let email: String?
  let status: String?
}

nonisolated struct HomeCalendarEvent: Codable, Sendable, Equatable, Identifiable {
  let key: String?
  let id: Int64
  let cal: Int64
  let calendar: String?
  let account: String?
  let title: String
  let location: String?
  let notes: String?
  let begin: Int64
  let end: Int64
  let allDay: Bool?
  let tz: String?
  let status: String?
  let selfResponse: String?
  let busy: Bool?
  let organizer: String?
  let attendees: [HomeCalendarAttendee]?

  var isLive: Bool { status != "cancelled" && selfResponse != "declined" }

  private enum CodingKeys: String, CodingKey {
    case key, id, cal, calendar, account, title, location, notes, begin, end, allDay, tz, status
    case selfResponse = "self"
    case busy, organizer, attendees
  }
}

nonisolated struct HomeSnapshot: Codable, Sendable, Equatable {
  let at: Int64
  let prices: [String: HomePrice]
  let marketCode: String?
  let marketValue: Double?
  let marketChange: Double?
  let brief: String
  let briefAt: Int64
  let briefStories: [Int64]
  let top: [HomeStory]
  let forYou: HomeForYou?
  let calendar: [HomeCalendarEvent]?

  /// The durable copy contains only public feed data. Calendar and FOR YOU stay in memory.
  var publicCacheCopy: Self {
    Self(
      at: at,
      prices: prices,
      marketCode: marketCode,
      marketValue: marketValue,
      marketChange: marketChange,
      brief: brief,
      briefAt: briefAt,
      briefStories: briefStories,
      top: top,
      forYou: nil,
      calendar: nil
    )
  }
}

nonisolated struct HomeRateRow: Codable, Sendable, Equatable {
  let price: Double
  let at: Int64
  let n: Int
  let spread: Double
  let used: String
  let change24: Double
  let hasChange: Bool
  let markets: Int
  let paths: String

  var change: Double? { hasChange ? change24 : nil }
}

nonisolated struct HomeMarket: Codable, Sendable, Equatable {
  let code: String
  let value: Double
  let dayChange: Double
  let constituents: Int
  let priced: Int
  let month: String
}

nonisolated struct HomeRates: Codable, Sendable, Equatable {
  let fxDay: String
  let fx: [String: Double]
  let index: [String: HomeRateRow]
  let btcUsd: Double
  let btcAt: Int64
  let btcN: Int
  let btcSpread: Double
  let btcUsed: String
  let ranksAt: Int64
  let ranksSource: String
  let days: Int
  let fxDays: Int
  let market: HomeMarket?
}

nonisolated struct HomeFastRates: Decodable, Sendable, Equatable {
  let at: Int64
  let index: [String: HomeRateRow]

  var atSeconds: Int64 { at > 10_000_000_000 ? at / 1_000 : at }
}

nonisolated struct HomeNewsItem: Codable, Sendable, Equatable {
  let feed: String
  let outlet: String
  let title: String
  let link: String
  let summary: String
  let published: Int64
}

nonisolated struct HomeNewsStory: Codable, Sendable, Equatable, Identifiable {
  let id: Int64
  let title: String
  let summary: String
  let sources: Int
  let firstSeen: Int64
  let lastSeen: Int64
  let items: [HomeNewsItem]

  private enum CodingKeys: String, CodingKey {
    case id, title, summary, sources, firstSeen, lastSeen, items
  }

  init(from decoder: Decoder) throws {
    let values = try decoder.container(keyedBy: CodingKeys.self)
    id = try values.decode(Int64.self, forKey: .id)
    title = try values.decode(String.self, forKey: .title)
    summary = try values.decode(String.self, forKey: .summary)
    sources = try values.decode(Int.self, forKey: .sources)
    firstSeen = try values.decode(Int64.self, forKey: .firstSeen)
    lastSeen = try values.decode(Int64.self, forKey: .lastSeen)
    items = try values.decodeIfPresent([HomeNewsItem].self, forKey: .items) ?? []
  }

  init(
    id: Int64,
    title: String,
    summary: String,
    sources: Int,
    firstSeen: Int64,
    lastSeen: Int64,
    items: [HomeNewsItem]
  ) {
    self.id = id
    self.title = title
    self.summary = summary
    self.sources = sources
    self.firstSeen = firstSeen
    self.lastSeen = lastSeen
    self.items = items
  }

  var lead: String {
    summary.split(separator: "\n", maxSplits: 1).first.map(String.init)?.trimmingCharacters(in: .whitespaces) ?? ""
  }
}

nonisolated struct HomeNews: Codable, Sendable, Equatable {
  let stories: [HomeNewsStory]
  let lastFetch: Int64
  let lastDigest: Int64
  let brief: String
  let briefAt: Int64
  let briefStories: [Int64]

  init(
    stories: [HomeNewsStory],
    lastFetch: Int64,
    lastDigest: Int64,
    brief: String = "",
    briefAt: Int64 = 0,
    briefStories: [Int64] = []
  ) {
    self.stories = stories
    self.lastFetch = lastFetch
    self.lastDigest = lastDigest
    self.brief = brief
    self.briefAt = briefAt
    self.briefStories = briefStories
  }

  init(from decoder: Decoder) throws {
    let values = try decoder.container(keyedBy: CodingKeys.self)
    stories = try values.decodeIfPresent([HomeNewsStory].self, forKey: .stories) ?? []
    lastFetch = try values.decodeIfPresent(Int64.self, forKey: .lastFetch) ?? 0
    lastDigest = try values.decodeIfPresent(Int64.self, forKey: .lastDigest) ?? 0
    brief = try values.decodeIfPresent(String.self, forKey: .brief) ?? ""
    briefAt = try values.decodeIfPresent(Int64.self, forKey: .briefAt) ?? 0
    briefStories = try values.decodeIfPresent([Int64].self, forKey: .briefStories) ?? []
  }
}

nonisolated struct HomeWeatherDay: Codable, Sendable, Equatable, Identifiable {
  let date: String
  let code: Int
  let maxC: Double
  let minC: Double
  let rainPct: Int
  let precipMm: Double?
  let sunrise: String?
  let sunset: String?

  var id: String { date }
}

nonisolated struct HomeWeatherHour: Codable, Sendable, Equatable, Identifiable {
  let at: String
  let tempC: Double
  let rainPct: Int?
  let precipMm: Double?
  let code: Int
  let windKmh: Double
  let spreadC: Double?

  var id: String { at }
}

nonisolated struct HomeWeather: Codable, Sendable, Equatable {
  nonisolated struct Forecast: Codable, Sendable, Equatable {
    nonisolated struct Now: Codable, Sendable, Equatable {
      let tempC: Double
      let feelsC: Double?
      let code: Int
      let windKmh: Double
      let humidity: Int?
    }

    nonisolated struct Place: Codable, Sendable, Equatable {
      let name: String
      let country: String?
    }

    let now: Now?
    let place: Place?
    let days: [HomeWeatherDay]?
    let hours: [HomeWeatherHour]?
    let fetchedAt: Int64?
    let utcOffset: Int?
    let source: String?
    let here: Bool?
    let timezone: String?
  }

  nonisolated struct Table: Codable, Sendable, Equatable {
    let fetchedAt: Int64?
    let places: Int64?
  }

  let ok: Bool
  let noGeo: Bool?
  let text: String?
  let forecast: Forecast?
  let table: Table?
}

nonisolated struct HomeBriefResult: Decodable, Sendable, Equatable {
  let written: Bool
  let why: String
  let brief: String
  let briefAt: Int64
  let briefStories: [Int64]
}

nonisolated struct HomeFetchSource: Decodable, Sendable, Equatable {
  let id: String
  let name: String
  let url: String
  let every: Int?
}

nonisolated struct HomeFetchList: Decodable, Sendable, Equatable {
  let feeds: [HomeFetchSource]
  let rates: [HomeFetchSource]
  let feedsEvery: Int
}

nonisolated struct HomeFetchedSource: Encodable, Sendable, Equatable {
  let id: String
  let status: Int
  let error: String?
  let body: String?
  let tookMs: Int64?
}

nonisolated struct HomeFeedOutcome: Sendable, Equatable {
  let at: Int64
  let feeds: Int
  let feedsOK: Int
  let rates: Int
  let ratesOK: Int
  let note: String
}

private nonisolated struct HomeNetworkBody: Encodable, Sendable { let net: String }
private nonisolated struct HomeOKAnswer: Decodable, Sendable { let ok: Bool }
private nonisolated struct HomeRatesFetchedBody: Encodable, Sendable {
  let fetchedAt: Int64
  let sources: [HomeFetchedSource]
}
private nonisolated struct HomeNewsFetchedBody: Encodable, Sendable {
  let fetchedAt: Int64
  let feeds: [HomeFetchedSource]
}

nonisolated enum HomeFeedError: Error, LocalizedError {
  case unsupported

  var errorDescription: String? { "This client cannot refresh the public feeds." }
}

protocol HomeServing: Sendable {
  func fetchHome() async throws -> HomeSnapshot
  func fetchRates() async throws -> HomeRates
  func fetchFastRates() async throws -> HomeFastRates
  func fetchNews(since: Int64) async throws -> HomeNews
  func fetchWeather(at location: HomeLocation) async throws -> HomeWeather
  func writeNewsBrief() async throws -> HomeBriefResult
  func reportPhoneNetwork(_ kind: String) async throws -> Bool
  func fetchList() async throws -> HomeFetchList
  func postFetchedRates(at: Int64, rows: [HomeFetchedSource]) async throws -> Bool
  func postFetchedNews(at: Int64, rows: [HomeFetchedSource]) async throws -> Bool
}

extension HomeServing {
  func reportPhoneNetwork(_: String) async throws -> Bool { throw HomeFeedError.unsupported }
  func fetchList() async throws -> HomeFetchList { throw HomeFeedError.unsupported }
  func postFetchedRates(at _: Int64, rows _: [HomeFetchedSource]) async throws -> Bool {
    throw HomeFeedError.unsupported
  }
  func postFetchedNews(at _: Int64, rows _: [HomeFetchedSource]) async throws -> Bool {
    throw HomeFeedError.unsupported
  }
}

extension BoxClient: HomeServing {
  func fetchHome() async throws -> HomeSnapshot {
    try await get("/v1/home")
  }

  func fetchRates() async throws -> HomeRates {
    try await get("/v1/rates")
  }

  func fetchFastRates() async throws -> HomeFastRates {
    try await get("/v1/rates/fast")
  }

  func fetchNews(since: Int64) async throws -> HomeNews {
    try await get("/v1/news?since=\(since)")
  }

  func fetchWeather(at location: HomeLocation) async throws -> HomeWeather {
    var components = URLComponents()
    components.path = "/v1/weather"
    components.queryItems = [
      URLQueryItem(name: "lat", value: String(location.latitude)),
      URLQueryItem(name: "lon", value: String(location.longitude)),
    ]
    return try await get(components.string ?? "/v1/weather")
  }

  func writeNewsBrief() async throws -> HomeBriefResult {
    try await post("/v1/news/brief", body: [String: String](), timeout: 180)
  }

  func reportPhoneNetwork(_ kind: String) async throws -> Bool {
    let answer: HomeOKAnswer = try await post(
      "/v1/phone/net",
      body: HomeNetworkBody(net: kind),
      timeout: 10
    )
    return answer.ok
  }

  func fetchList() async throws -> HomeFetchList {
    try await get("/v1/fetch/list")
  }

  func postFetchedRates(at: Int64, rows: [HomeFetchedSource]) async throws -> Bool {
    let answer: HomeOKAnswer = try await post(
      "/v1/rates/fetched",
      body: HomeRatesFetchedBody(fetchedAt: at, sources: rows),
      timeout: 60
    )
    return answer.ok
  }

  func postFetchedNews(at: Int64, rows: [HomeFetchedSource]) async throws -> Bool {
    let answer: HomeOKAnswer = try await post(
      "/v1/news/fetched",
      body: HomeNewsFetchedBody(fetchedAt: at, feeds: rows),
      timeout: 60
    )
    return answer.ok
  }
}

protocol HomeFeedRefreshing: Sendable {
  func run(client: any HomeServing) async -> HomeFeedOutcome
}

protocol HomePublicFetching: Sendable {
  func fetch(_ source: HomeFetchSource) async -> HomeFetchedSource
}

/// The public-feed destination rule. URLSession does not expose its connected peer address, so a
/// preflight lookup is the strongest available check without replacing its TLS/HTTP transport.
/// The syntactic check still runs for every redirect, and the lookup is repeated for its new host.
nonisolated enum HomePublicDestinationPolicy {
  private enum Resolution: Sendable {
    case addresses([String])
    case failed
  }

  private static let localNameSuffixes = [
    ".localhost", ".local", ".internal", ".lan", ".home", ".home.arpa",
    ".intranet", ".corp", ".test", ".invalid", ".onion",
  ]

  static func validatedURL(_ raw: String) -> URL? {
    guard !raw.isEmpty,
          raw.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
          raw.rangeOfCharacter(from: .controlCharacters) == nil,
          !raw.contains("\\"),
          let components = URLComponents(string: raw),
          components.scheme?.lowercased() == "https",
          let host = components.host,
          !host.isEmpty,
          host.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
          host.rangeOfCharacter(from: .controlCharacters) == nil,
          !host.contains("\\"),
          components.user == nil,
          components.password == nil,
          hostIsSyntacticallyPublic(host)
    else { return nil }
    return components.url
  }

  static func validatedRedirectURL(_ raw: String, hop: Int) -> URL? {
    guard (1...5).contains(hop) else { return nil }
    return validatedURL(raw)
  }

  static func allows(_ url: URL) async -> Bool {
    guard validatedURL(url.absoluteString) != nil, let host = url.host else { return false }
    if let literal = literalAddress(host) { return addressIsPublic(literal) }
    let resolution = await resolveWithTimeout(host)
    guard case .addresses(let addresses) = resolution else { return false }
    return addressesArePublic(addresses)
  }

  static func addressesArePublic(_ addresses: [String]) -> Bool {
    !addresses.isEmpty && addresses.allSatisfy {
      guard let address = literalAddress($0) else { return false }
      return addressIsPublic(address)
    }
  }

  private static func hostIsSyntacticallyPublic(_ rawHost: String) -> Bool {
    let host = rawHost.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "."))
    guard !host.isEmpty else { return false }
    if let literal = literalAddress(host) { return addressIsPublic(literal) }
    guard host != "localhost", host.contains(".") else { return false }
    return !localNameSuffixes.contains { host.hasSuffix($0) }
  }

  private static func literalAddress(_ raw: String) -> [UInt8]? {
    let unbracketed = raw.hasPrefix("[") && raw.hasSuffix("]")
      ? String(raw.dropFirst().dropLast())
      : raw
    let host = unbracketed.split(separator: "%", maxSplits: 1).first.map(String.init) ?? unbracketed
    if let address = IPv4Address(host) { return Array(address.rawValue) }
    if let address = IPv6Address(host) { return Array(address.rawValue) }
    return nil
  }

  private static func addressIsPublic(_ bytes: [UInt8]) -> Bool {
    if bytes.count == 4 { return ipv4IsPublic(bytes) }
    guard bytes.count == 16 else { return false }
    if bytes[0..<10].allSatisfy({ $0 == 0 }), bytes[10] == 0xff, bytes[11] == 0xff {
      return ipv4IsPublic(Array(bytes[12..<16]))
    }
    if bytes.allSatisfy({ $0 == 0 }) || bytes.dropLast().allSatisfy({ $0 == 0 }) && bytes.last == 1 {
      return false
    }
    if bytes[0] == 0xff || bytes[0] & 0xfe == 0xfc || bytes[0] == 0xfe && bytes[1] & 0xc0 == 0x80 {
      return false
    }
    if bytes[0] == 0xfe && bytes[1] & 0xc0 == 0xc0 { return false } // retired site-local
    if bytes[0] == 0x00 && bytes[1] == 0x64 && bytes[2] == 0xff && bytes[3] == 0x9b {
      // 64:ff9b::/96, the well-known NAT64 prefix: an IPv4 address in its last four bytes. The
      // box refuses the whole prefix, and a box is never behind NAT64; a phone on an IPv6-only
      // mobile network is, and there every IPv4-only feed resolves into it. So it is judged by
      // the address it carries, the way an IPv4-mapped one is above: a public one passes, and
      // 64:ff9b::7f00:1 (127.0.0.1 in disguise) does not.
      if bytes[4..<12].allSatisfy({ $0 == 0 }) { return ipv4IsPublic(Array(bytes[12..<16])) }
      if bytes[4] == 0x00 && bytes[5] == 0x01 { return false } // 64:ff9b:1::/48, local use
    }
    if bytes[0] == 0x20 && bytes[1] == 0x02 { return false } // 6to4
    if bytes[0...3] == [0x20, 0x01, 0x00, 0x00] { return false } // Teredo
    if bytes[0...3] == [0x20, 0x01, 0x0d, 0xb8] { return false } // documentation
    if bytes[0] == 0x01 && bytes[1..<8].allSatisfy({ $0 == 0 }) { return false } // discard
    return true
  }

  private static func ipv4IsPublic(_ bytes: [UInt8]) -> Bool {
    guard bytes.count == 4 else { return false }
    switch bytes[0] {
    case 0, 10, 127: return false
    case 100 where (64...127).contains(bytes[1]): return false
    case 169 where bytes[1] == 254: return false
    case 172 where (16...31).contains(bytes[1]): return false
    case 192 where bytes[1] == 168: return false
    case 192 where bytes[1] == 0 && (bytes[2] == 0 || bytes[2] == 2): return false
    case 198 where bytes[1] == 18 || bytes[1] == 19: return false
    case 198 where bytes[1] == 51 && bytes[2] == 100: return false
    case 203 where bytes[1] == 0 && bytes[2] == 113: return false
    case 224...255: return false
    default: return true
    }
  }

  private static func resolveWithTimeout(_ host: String) async -> Resolution {
    let stream = AsyncStream<Resolution> { continuation in
      DispatchQueue.global(qos: .utility).async {
        if let addresses = resolve(host) {
          continuation.yield(.addresses(addresses))
        } else {
          continuation.yield(.failed)
        }
        continuation.finish()
      }
    }
    return await withTaskGroup(of: Resolution.self, returning: Resolution.self) { group in
      group.addTask {
        var iterator = stream.makeAsyncIterator()
        return await iterator.next() ?? .failed
      }
      group.addTask {
        try? await Task.sleep(for: .seconds(3))
        return .failed
      }
      let answer = await group.next() ?? .failed
      group.cancelAll()
      return answer
    }
  }

  private static func resolve(_ host: String) -> [String]? {
    var hints = addrinfo()
    hints.ai_flags = AI_ADDRCONFIG
    hints.ai_family = AF_UNSPEC
    hints.ai_socktype = SOCK_STREAM
    hints.ai_protocol = IPPROTO_TCP
    var first: UnsafeMutablePointer<addrinfo>?
    let status = host.withCString { getaddrinfo($0, nil, &hints, &first) }
    guard status == 0, let first else { return nil }
    defer { freeaddrinfo(first) }

    var addresses: [String] = []
    var current: UnsafeMutablePointer<addrinfo>? = first
    while let info = current {
      if let address = info.pointee.ai_addr {
        var buffer = [CChar](repeating: 0, count: Int(NI_MAXHOST))
        if getnameinfo(
          address,
          info.pointee.ai_addrlen,
          &buffer,
          socklen_t(buffer.count),
          nil,
          0,
          NI_NUMERICHOST
        ) == 0 {
          addresses.append(String(
            decoding: buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) },
            as: UTF8.self
          ))
        }
      }
      current = info.pointee.ai_next
    }
    return addresses
  }
}

/// The phone fetches only the public sources named by the paired box, then returns their raw bytes
/// over the pinned channel. No cookie, credential, cache entry or personal data goes to a source.
nonisolated struct HomeFeedRefresher: HomeFeedRefreshing {
  private static let maxRateSources = 32
  private static let maxFeedSources = 64
  private static let maxRatesPayload = 3_500_000
  private static let maxNewsPayload = 15_000_000
  private static let maxSourceIDBytes = 40
  private let fetcher: any HomePublicFetching
  private let networkKind: @Sendable () async -> String

  init(
    fetcher: any HomePublicFetching = HomePublicSourceFetcher(),
    networkKind: @escaping @Sendable () async -> String = {
      await HomeFeedRefresher.phoneNetworkKind()
    }
  ) {
    self.fetcher = fetcher
    self.networkKind = networkKind
  }

  func run(client: any HomeServing) async -> HomeFeedOutcome {
    let at = Int64(Date().timeIntervalSince1970)
    _ = try? await client.reportPhoneNetwork(networkKind())

    let list: HomeFetchList
    do {
      list = try await client.fetchList()
    } catch is CancellationError {
      return outcome(at: at, note: "cancelled")
    } catch {
      return outcome(at: at, note: "the box did not answer (is it unlocked?)")
    }

    let rates = await fetchRates(list.rates, client: client, at: at)
    guard !Task.isCancelled else {
      return outcome(at: at, rates: rates.count, ratesOK: rates.ok, note: "cancelled")
    }
    let feeds = await fetchFeeds(list.feeds, client: client, at: at)
    let truncated = list.rates.count > Self.maxRateSources || list.feeds.count > Self.maxFeedSources
      || rates.omitted > 0 || feeds.omitted > 0
    let note = Task.isCancelled
      ? "cancelled"
      : feeds.accepted
        ? (truncated ? "the box listed more public sources than one bounded refresh can fetch" : "")
        : "fetched, but the box did not take the feeds"
    return outcome(
      at: at,
      feeds: feeds.count,
      feedsOK: feeds.ok,
      rates: rates.count,
      ratesOK: rates.ok,
      note: note
    )
  }

  private func fetchRates(
    _ sources: [HomeFetchSource],
    client: any HomeServing,
    at: Int64
  ) async -> (count: Int, ok: Int, omitted: Int) {
    var chunk: [HomeFetchedSource] = []
    var count = 0
    var ok = 0
    var omitted = 0
    for source in sources.prefix(Self.maxRateSources) {
      guard !Task.isCancelled else { break }
      guard Self.validSourceID(source.id) else {
        omitted += 1
        continue
      }
      var row = await fetcher.fetch(source)
      guard !Task.isCancelled else { break }
      if !Self.ratesPayloadFits([row], at: at) { row = Self.oversized(row) }
      guard Self.ratesPayloadFits([row], at: at) else {
        omitted += 1
        continue
      }
      if !chunk.isEmpty, !Self.ratesPayloadFits(chunk + [row], at: at) {
        guard !Task.isCancelled else { break }
        _ = try? await client.postFetchedRates(at: at, rows: chunk)
        chunk.removeAll(keepingCapacity: true)
      }
      chunk.append(row)
      count += 1
      if Self.isOK(row) { ok += 1 }
    }
    if !Task.isCancelled, !chunk.isEmpty {
      _ = try? await client.postFetchedRates(at: at, rows: chunk)
    }
    return (count, ok, omitted)
  }

  private func fetchFeeds(
    _ sources: [HomeFetchSource],
    client: any HomeServing,
    at: Int64
  ) async -> (count: Int, ok: Int, omitted: Int, accepted: Bool) {
    var chunk: [HomeFetchedSource] = []
    var count = 0
    var ok = 0
    var omitted = 0
    for source in sources.prefix(Self.maxFeedSources) {
      guard !Task.isCancelled else { break }
      guard Self.validSourceID(source.id) else {
        omitted += 1
        continue
      }
      var row = await fetcher.fetch(source)
      guard !Task.isCancelled else { break }
      if !Self.newsPayloadFits([row], at: at) { row = Self.oversized(row) }
      guard Self.newsPayloadFits([row], at: at) else {
        omitted += 1
        continue
      }
      if !chunk.isEmpty, !Self.newsPayloadFits(chunk + [row], at: at) {
        guard !Task.isCancelled else { return (count, ok, omitted, false) }
        do {
          guard try await client.postFetchedNews(at: at, rows: chunk) else {
            return (count, ok, omitted, false)
          }
        } catch {
          return (count, ok, omitted, false)
        }
        chunk.removeAll(keepingCapacity: true)
      }
      chunk.append(row)
      count += 1
      if Self.isOK(row) { ok += 1 }
    }
    guard !Task.isCancelled else { return (count, ok, omitted, false) }
    guard !chunk.isEmpty else { return (count, ok, omitted, true) }
    do {
      return (count, ok, omitted, try await client.postFetchedNews(at: at, rows: chunk))
    } catch {
      return (count, ok, omitted, false)
    }
  }

  private static func isOK(_ row: HomeFetchedSource) -> Bool {
    (200..<300).contains(row.status) && row.body?.isEmpty == false
  }

  private static func validSourceID(_ id: String) -> Bool {
    !id.isEmpty && id.utf8.count <= maxSourceIDBytes
  }

  private static func oversized(_ row: HomeFetchedSource) -> HomeFetchedSource {
    HomeFetchedSource(
      id: row.id,
      status: 0,
      error: "body does not fit a bounded box batch",
      body: nil,
      tookMs: row.tookMs
    )
  }

  private static func ratesPayloadFits(_ rows: [HomeFetchedSource], at: Int64) -> Bool {
    (try? JSONEncoder().encode(HomeRatesFetchedBody(fetchedAt: at, sources: rows)).count)
      .map { $0 <= maxRatesPayload } ?? false
  }

  private static func newsPayloadFits(_ rows: [HomeFetchedSource], at: Int64) -> Bool {
    (try? JSONEncoder().encode(HomeNewsFetchedBody(fetchedAt: at, feeds: rows)).count)
      .map { $0 <= maxNewsPayload } ?? false
  }

  private func outcome(
    at: Int64,
    feeds: Int = 0,
    feedsOK: Int = 0,
    rates: Int = 0,
    ratesOK: Int = 0,
    note: String
  ) -> HomeFeedOutcome {
    HomeFeedOutcome(
      at: at,
      feeds: feeds,
      feedsOK: feedsOK,
      rates: rates,
      ratesOK: ratesOK,
      note: note
    )
  }

  static func publicSourceURL(_ raw: String) -> URL? {
    HomePublicDestinationPolicy.validatedURL(raw)
  }

  private static func phoneNetworkKind() async -> String {
    let monitor = NWPathMonitor()
    let stream = AsyncStream<String> { continuation in
      continuation.onTermination = { _ in monitor.cancel() }
      monitor.pathUpdateHandler = { path in
        continuation.yield(Self.kind(for: path))
        continuation.finish()
      }
      monitor.start(queue: DispatchQueue(label: "ai.localghost.home-network-probe"))
    }
    return await withTaskGroup(of: String.self, returning: String.self) { group in
      group.addTask {
        var iterator = stream.makeAsyncIterator()
        return await iterator.next() ?? "none"
      }
      group.addTask {
        try? await Task.sleep(for: .seconds(2))
        return "none"
      }
      let answer = await group.next() ?? "none"
      group.cancelAll()
      return answer
    }
  }

  private static func kind(for path: NWPath) -> String {
    guard path.status == .satisfied else { return "none" }
    if path.usesInterfaceType(.wifi) || path.usesInterfaceType(.wiredEthernet) { return "wifi" }
    if path.usesInterfaceType(.cellular) { return "mobile" }
    return "none"
  }
}

nonisolated struct HomePublicSourceFetcher: HomePublicFetching {
  private static let bodyCap = 3_000_000
  private static let userAgent =
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) " +
    "AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"

  func fetch(_ source: HomeFetchSource) async -> HomeFetchedSource {
    let started = ContinuousClock.now
    guard let url = HomeFeedRefresher.publicSourceURL(source.url) else {
      return HomeFetchedSource(
        id: source.id,
        status: 0,
        error: "invalid public HTTPS address",
        body: nil,
        tookMs: 0
      )
    }
    guard await HomePublicDestinationPolicy.allows(url) else {
      return HomeFetchedSource(
        id: source.id,
        status: 0,
        error: "public address could not be verified",
        body: nil,
        tookMs: 0
      )
    }
    var request = URLRequest(url: url)
    request.httpMethod = "GET"
    request.timeoutInterval = 10
    request.cachePolicy = .reloadIgnoringLocalCacheData
    request.setValue(Self.userAgent, forHTTPHeaderField: "User-Agent")
    request.setValue(
      "application/rss+xml, application/atom+xml, application/xml, application/json, " +
        "text/xml;q=0.9, */*;q=0.5",
      forHTTPHeaderField: "Accept"
    )
    let raw = await HomeLimitedDownload(cap: Self.bodyCap).load(request)
    let elapsed = started.duration(to: .now)
    let tookMs = Int64(elapsed.components.seconds) * 1_000
      + Int64(elapsed.components.attoseconds / 1_000_000_000_000_000)
    return HomeFetchedSource(
      id: source.id,
      status: raw.status,
      error: raw.error.isEmpty ? nil : String(raw.error.prefix(160)),
      body: raw.data.isEmpty ? nil : String(decoding: raw.data, as: UTF8.self),
      tookMs: tookMs > 0 ? tookMs : nil
    )
  }
}

private nonisolated struct HomeRawFetch: Sendable {
  let status: Int
  let data: Data
  let error: String
}

private nonisolated final class HomeLimitedDownload: NSObject, URLSessionDataDelegate, @unchecked Sendable {
  private let cap: Int
  private let stateLock = NSLock()
  private var bytes = Data()
  private var response: HTTPURLResponse?
  private var continuation: CheckedContinuation<HomeRawFetch, Never>?
  private var session: URLSession?
  private var task: URLSessionDataTask?
  private var cancelRequested = false
  private var reachedCap = false
  private var redirectCount = 0

  init(cap: Int) { self.cap = cap }

  func load(_ request: URLRequest) async -> HomeRawFetch {
    await withTaskCancellationHandler {
      await withCheckedContinuation { continuation in
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 10
        configuration.timeoutIntervalForResource = 15
        configuration.waitsForConnectivity = false
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.urlCache = nil
        configuration.httpCookieStorage = nil
        configuration.httpShouldSetCookies = false
        configuration.urlCredentialStorage = nil
        configuration.connectionProxyDictionary = [:]
        let session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
        let task = session.dataTask(with: request)
        stateLock.lock()
        self.continuation = continuation
        self.session = session
        self.task = task
        let shouldCancel = cancelRequested
        stateLock.unlock()
        task.resume()
        if shouldCancel { task.cancel() }
      }
    } onCancel: {
      stateLock.lock()
      cancelRequested = true
      let task = self.task
      stateLock.unlock()
      task?.cancel()
    }
  }

  func urlSession(
    _: URLSession,
    dataTask _: URLSessionDataTask,
    didReceive response: URLResponse,
    completionHandler: @escaping @Sendable (URLSession.ResponseDisposition) -> Void
  ) {
    self.response = response as? HTTPURLResponse
    completionHandler(.allow)
  }

  func urlSession(_: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
    let room = cap - bytes.count
    if room > 0 { bytes.append(data.prefix(room)) }
    if bytes.count >= cap {
      reachedCap = true
      dataTask.cancel()
    }
  }

  func urlSession(_: URLSession, task _: URLSessionTask, didCompleteWithError error: Error?) {
    stateLock.lock()
    guard let continuation else {
      stateLock.unlock()
      return
    }
    self.continuation = nil
    self.task = nil
    let session = self.session
    self.session = nil
    stateLock.unlock()
    let answer: HomeRawFetch
    if let error, !reachedCap {
      answer = HomeRawFetch(status: 0, data: Data(), error: error.localizedDescription)
    } else {
      answer = HomeRawFetch(status: response?.statusCode ?? 0, data: bytes, error: "")
    }
    session?.finishTasksAndInvalidate()
    continuation.resume(returning: answer)
  }

  func urlSession(
    _: URLSession,
    task _: URLSessionTask,
    willPerformHTTPRedirection _: HTTPURLResponse,
    newRequest request: URLRequest,
    completionHandler: @escaping @Sendable (URLRequest?) -> Void
  ) {
    redirectCount += 1
    guard let rawURL = request.url,
          let url = HomePublicDestinationPolicy.validatedRedirectURL(
            rawURL.absoluteString,
            hop: redirectCount
          )
    else {
      completionHandler(nil)
      return
    }
    Task {
      let allowed = await HomePublicDestinationPolicy.allows(url)
      completionHandler(allowed && !cancellationWasRequested() ? request : nil)
    }
  }

  private func cancellationWasRequested() -> Bool {
    stateLock.lock()
    defer { stateLock.unlock() }
    return cancelRequested
  }
}
