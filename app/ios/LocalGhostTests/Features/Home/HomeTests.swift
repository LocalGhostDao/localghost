import Foundation
import Testing

@testable import LocalGhost

private func homeWeatherFixture() -> HomeWeather {
  HomeWeather(
    ok: true,
    noGeo: false,
    text: nil,
    forecast: HomeWeather.Forecast(
      now: HomeWeather.Forecast.Now(
        tempC: 28.5,
        feelsC: 31.0,
        code: 2,
        windKmh: 12,
        humidity: 78
      ),
      place: HomeWeather.Forecast.Place(name: "Lagos", country: "Nigeria"),
      days: [],
      hours: [
        HomeWeatherHour(
          at: "2026-11-18T12:00",
          tempC: 28.5,
          rainPct: 20,
          precipMm: 0.1,
          code: 2,
          windKmh: 12,
          spreadC: 1.2
        ),
      ],
      fetchedAt: 1_795_000_000,
      utcOffset: 3_600,
      source: "test",
      here: true,
      timezone: "Africa/Lagos"
    ),
    table: nil
  )
}

struct HomePrivacyTests {
  @Test
  func publicCacheCopyDropsPersonalRows() throws {
    let snapshot = try fixture()

    let cached = snapshot.publicCacheCopy

    #expect(cached.forYou == nil)
    #expect(cached.calendar == nil)
    #expect(cached.at == snapshot.at)
    #expect(cached.prices == snapshot.prices)
    #expect(cached.brief == snapshot.brief)
    #expect(cached.top == snapshot.top)
  }

  @Test
  func cacheNeverWritesPersonalHomeRows() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-tests-\(UUID().uuidString)", isDirectory: true)
    let fileURL = directory.appendingPathComponent("home.json")
    defer { try? FileManager.default.removeItem(at: directory) }
    let cache = HomeCache(fileURL: fileURL)

    let weather = homeWeatherFixture()
    try await cache.save(snapshot: fixture(), rates: nil, news: nil, weather: weather)

    let bytes = try Data(contentsOf: fileURL)
    let raw = try #require(String(data: bytes, encoding: .utf8))
    #expect(!raw.contains("private appointment"))
    #expect(!raw.contains("the park"))
    #expect(!raw.contains("private memory"))
    let restored = await cache.load()
    #expect(restored?.snapshot?.forYou == nil)
    #expect(restored?.snapshot?.calendar == nil)
    #expect(restored?.snapshot?.brief == "public brief")
    #expect(restored?.weather == weather)
  }

  @Test(arguments: [false, true])
  @MainActor
  func aLateHomeResponseCannotRewriteAForgottenCache(enrolAgain: Bool) async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-forgotten-cache-tests-\(UUID().uuidString)", isDirectory: true)
    let file = directory.appendingPathComponent("home.json")
    defer { try? FileManager.default.removeItem(at: directory) }
    let oldCache = HomeCache(fileURL: file)
    try await oldCache.save(snapshot: fixture(), rates: nil, news: nil, weather: nil)
    let gate = HomeTestGate()
    let client = BlockingHomeClient(gate: gate)
    let model = HomeModel(client: { client }, cache: oldCache)
    let refresh = Task { await model.refresh() }
    await gate.waitUntilBlocked()

    try await oldCache.invalidate()
    #expect(!FileManager.default.fileExists(atPath: file.path))
    let newCache = HomeCache(fileURL: file)
    let weather = homeWeatherFixture()
    if enrolAgain {
      try await newCache.save(snapshot: nil, rates: nil, news: nil, weather: weather)
    }

    // This refresh is deliberately not cancelled: old responses can arrive after teardown.
    await gate.release()
    await refresh.value
    #expect(await oldCache.load() == nil)
    if enrolAgain {
      #expect(await newCache.load()?.weather == weather)
    } else {
      #expect(!FileManager.default.fileExists(atPath: file.path))
    }
  }

  @Test
  func forgettingSerializesWithConcurrentCacheSaves() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-cache-save-race-tests-\(UUID().uuidString)", isDirectory: true)
    let file = directory.appendingPathComponent("home.json")
    defer { try? FileManager.default.removeItem(at: directory) }
    let cache = HomeCache(fileURL: file)
    try await cache.save(snapshot: nil, rates: nil, news: nil, weather: nil)
    try await withThrowingTaskGroup(of: Void.self) { group in
      for _ in 0..<20 {
        group.addTask { try await cache.save(snapshot: nil, rates: nil, news: nil, weather: nil) }
      }
      try await cache.invalidate()
      try await group.waitForAll()
    }
    #expect(!FileManager.default.fileExists(atPath: file.path))
    try await cache.invalidate() // Repeating a forget is harmless.
  }

  @Test
  func cacheDecodesFilesWrittenBeforeWeatherWasAdded() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-tests-\(UUID().uuidString)", isDirectory: true)
    let fileURL = directory.appendingPathComponent("home.json")
    defer { try? FileManager.default.removeItem(at: directory) }
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try Data(#"{"snapshot":null,"rates":null,"news":null}"#.utf8).write(to: fileURL)

    let restored = await HomeCache(fileURL: fileURL).load()

    #expect(restored != nil)
    #expect(restored?.weather == nil)
  }

  private func fixture() throws -> HomeSnapshot {
    let json = #"""
    {
      "at": 1700000000,
      "prices": {
        "BTC": {
          "price": 52000.25,
          "change24": 1.25,
          "hasChange": true,
          "at": 1699999990,
          "n": 4
        }
      },
      "marketCode": "LG",
      "marketValue": 100.0,
      "marketChange": 0.5,
      "brief": "public brief",
      "briefAt": 1699999900,
      "briefStories": [11],
      "top": [{
        "id": 11,
        "title": "public story",
        "lead": "public lead",
        "outlets": ["wire"],
        "sources": 1,
        "lastSeen": 1699999800
      }],
      "forYou": {
        "at": 1700000000,
        "from": 1699990000,
        "places": [{
          "name": "the park",
          "kind": "park",
          "km": 1.5,
          "bearing": "NW",
          "why": "private place",
          "new": true
        }],
        "days": [],
        "stories": [],
        "remember": {
          "id": 99,
          "kind": "memory",
          "title": "private memory",
          "body": "private body"
        },
        "note": "private note"
      },
      "calendar": [{
        "id": 1,
        "cal": 2,
        "title": "private appointment",
        "begin": 1700000100,
        "end": 1700003700
      }]
    }
    """#
    return try JSONDecoder().decode(HomeSnapshot.self, from: Data(json.utf8))
  }
}

struct HomeWireModelTests {
  @Test
  func homeToleratesPhotoOnlyDaysAndNilNestedLists() throws {
    let json = #"""
    {
      "at": 1700000000,
      "prices": {},
      "brief": "",
      "briefAt": 0,
      "briefStories": [],
      "top": [{
        "id": 1,
        "title": "story",
        "lead": "lead",
        "outlets": null,
        "sources": 1,
        "lastSeen": 1700000000
      }],
      "forYou": {
        "at": 1700000000,
        "from": 0,
        "places": [],
        "days": [{
          "day": "2026-11-18",
          "yearsAgo": 2,
          "title": "photos from this day",
          "lead": "",
          "photos": 3,
          "place": null
        }],
        "stories": [],
        "remember": null,
        "note": null
      },
      "calendar": null
    }
    """#

    let snapshot = try JSONDecoder().decode(HomeSnapshot.self, from: Data(json.utf8))

    #expect(snapshot.top.first?.outlets == [])
    #expect(snapshot.forYou?.days.first?.memoryId == nil)
  }

  @Test
  func newsToleratesNilItems() throws {
    let json = #"{"id":1,"title":"story","summary":"lead","sources":1,"firstSeen":1,"lastSeen":2,"items":null}"#

    let story = try JSONDecoder().decode(HomeNewsStory.self, from: Data(json.utf8))

    #expect(story.items.isEmpty)
  }

  @Test
  func anUnavailablePriceChangeIsNilRatherThanZero() throws {
    let json = #"{"price":42.5,"change24":0,"hasChange":false,"at":1234,"n":1}"#

    let price = try JSONDecoder().decode(HomePrice.self, from: Data(json.utf8))

    #expect(price.change == nil)
  }

  @Test
  func fastRateTimestampsAcceptSecondsAndMilliseconds() throws {
    let seconds = try JSONDecoder().decode(
      HomeFastRates.self,
      from: Data(#"{"at":1700000000,"index":{}}"#.utf8)
    )
    let milliseconds = try JSONDecoder().decode(
      HomeFastRates.self,
      from: Data(#"{"at":1700000000123,"index":{}}"#.utf8)
    )

    #expect(seconds.atSeconds == 1_700_000_000)
    #expect(milliseconds.atSeconds == 1_700_000_000)
  }

  @Test(arguments: [
    "https://example.com%0d%0a.evil",
    "https://example.com%09.evil",
    "https://example.com%20.evil",
    "https://example.com%5c.evil",
    "file:///private/etc/passwd",
    "https://user@example.com/feed",
    "https://localhost/feed",
    "https://LOCALHOST./feed",
    "https://box/feed",
    "https://printer.local/feed",
    "https://router.lan/feed",
    "https://db.internal/feed",
    "https://127.0.0.1/feed",
    "https://[::1]:8443/feed",
    "https://192.168.1.1/feed",
    "https://169.254.169.254/latest/meta-data/",
    "https://2130706433/feed",
    "https://x.onion/feed",
  ])
  func publicFeedSourcesRejectUnsafeAddresses(_ raw: String) {
    #expect(HomeFeedRefresher.publicSourceURL(raw) == nil)
  }

  @Test
  func publicFeedSourcesRequireCredentialFreeHTTPS() {
    #expect(HomeFeedRefresher.publicSourceURL("https://example.com/feed.xml") != nil)
    #expect(HomeFeedRefresher.publicSourceURL("https://1.1.1.1/feed.xml") != nil)
    #expect(HomeFeedRefresher.publicSourceURL("https://[2606:4700:4700::1111]/feed.xml") != nil)
    #expect(HomeFeedRefresher.publicSourceURL("http://example.com/feed.json") == nil)
  }

  @Test(arguments: [
    "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
    "100.64.0.1", "192.0.2.5", "198.18.0.1", "203.0.113.8", "224.0.0.1",
    "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1",
    "2002:7f00:1::1", "2001::1", "2001:db8::1", "fec0::1",
  ])
  func resolvedPublicFeedAddressesRejectNonPublicRanges(_ address: String) {
    #expect(!HomePublicDestinationPolicy.addressesArePublic([address]))
  }

  @Test
  func resolvedPublicFeedAddressesAcceptOnlyAllPublicAnswers() {
    #expect(HomePublicDestinationPolicy.addressesArePublic(["1.1.1.1", "2606:4700:4700::1111"]))
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["1.1.1.1", "127.0.0.1"]))
    #expect(!HomePublicDestinationPolicy.addressesArePublic([]))
  }

  @Test
  func aNAT64AddressIsJudgedByTheIPv4AddressItCarries() {
    // an IPv6-only mobile network hands an IPv4-only feed's address over inside 64:ff9b::/96
    #expect(HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::101:101"])) // 1.1.1.1
    #expect(HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::5db8:d822"])) // 93.184.216.34
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::7f00:1"])) // 127.0.0.1
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::a00:1"])) // 10.0.0.1
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::c0a8:101"])) // 192.168.1.1
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::a9fe:a9fe"])) // 169.254.169.254
    // the local-use prefix beside it carries whatever a network likes: still refused
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b:1::101:101"]))
    // and one private answer among public ones refuses the name, as before
    #expect(!HomePublicDestinationPolicy.addressesArePublic(["64:ff9b::101:101", "64:ff9b::c0a8:101"]))
  }

  @Test
  func redirectsUseTheSameDestinationRuleAndStopAfterFiveHops() {
    #expect(
      HomePublicDestinationPolicy.validatedRedirectURL(
        "https://example.com/feed.xml",
        hop: 5
      ) != nil
    )
    #expect(
      HomePublicDestinationPolicy.validatedRedirectURL(
        "https://127.0.0.1/feed.xml",
        hop: 1
      ) == nil
    )
    #expect(
      HomePublicDestinationPolicy.validatedRedirectURL(
        "https://example.com/feed.xml",
        hop: 6
      ) == nil
    )
  }
}

@MainActor
struct HomeWeatherCacheTests {
  @Test
  func coldLaunchKeepsCachedWeatherUntilLocationRefreshesIt() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-weather-cache-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let cache = HomeCache(fileURL: directory.appendingPathComponent("home.json"))
    let weather = homeWeatherFixture()
    try await cache.save(snapshot: nil, rates: nil, news: nil, weather: weather)
    let gate = HomeTestGate()
    let client = BlockingHomeClient(gate: gate)
    let model = HomeModel(
      client: { client },
      cache: cache,
      pollEvery: .seconds(3_600),
      fastEvery: .seconds(3_600),
      clockEvery: .seconds(3_600)
    )

    let run = Task { await model.run() }
    await gate.waitUntilBlocked()

    #expect(model.weather == weather)
    #expect(!model.locationSupplied)

    run.cancel()
    await gate.release()
    await run.value
  }
}

@MainActor
struct HomeModelRefreshTests {
  @Test
  func locationArrivalCoalescesBehindAnActiveRefresh() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-refresh-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let gate = HomeTestGate()
    let client = BlockingHomeClient(gate: gate)
    var location: HomeLocation?
    let model = HomeModel(
      client: { client },
      location: { location },
      cache: HomeCache(fileURL: directory.appendingPathComponent("home.json"))
    )

    let initial = Task { await model.refresh() }
    await gate.waitUntilBlocked()
    location = HomeLocation(latitude: 6.45, longitude: 3.39)
    let withLocation = Task { await model.refresh() }
    await gate.release()
    await initial.value
    await withLocation.value

    #expect(model.weather?.ok == true)
    #expect(model.locationSupplied)
    let expectedLocation = try #require(location)
    #expect(await client.weatherLocations() == [expectedLocation])
  }

  @Test
  func turningOffPublicFetchStillReadsTheBoxButNeverStartsAFeedFetch() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-feed-policy-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let client = StaleNewsHomeClient()
    let recorder = FeedRefreshRecorder()
    let model = HomeModel(
      client: { client }, cache: HomeCache(fileURL: directory.appendingPathComponent("home.json")),
      feedRefresher: CountingFeedRefresher(recorder: recorder), feedsEnabled: { false }
    )
    await model.refreshFromGesture()
    #expect(model.news != nil)
    #expect(!model.fetchingFeeds)
    #expect(await recorder.get() == 0)
  }

  @Test
  func staleNewsStartsTheForcedFeedLaneWithoutBlockingTheRefreshGesture() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-feed-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let gate = HomeTestGate()
    let recorder = FeedRefreshRecorder()
    let refresher = BlockingFeedRefresher(gate: gate, recorder: recorder)
    let client = StaleNewsHomeClient()
    let model = HomeModel(
      client: { client },
      cache: HomeCache(fileURL: directory.appendingPathComponent("home.json")),
      feedRefresher: refresher,
      feedSettleDelay: .zero
    )

    await model.refreshFromGesture()
    await gate.waitUntilBlocked()
    #expect(model.fetchingFeeds)
    #expect(await recorder.get() == 1)

    await gate.release()
    for _ in 0..<100 where model.fetchingFeeds { await Task.yield() }
    #expect(!model.fetchingFeeds)
  }

  @Test
  func manualRefreshWaitsForAnActiveFreshPollBeforeApplyingTheFeedGate() async throws {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-gate-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let gate = HomeTestGate()
    let recorder = FeedRefreshRecorder()
    let now = Date(timeIntervalSince1970: 1_800_000_000)
    let client = FreshNewsHomeClient(gate: gate, lastFetch: Int64(now.timeIntervalSince1970))
    let model = HomeModel(
      client: { client },
      cache: HomeCache(fileURL: directory.appendingPathComponent("home.json")),
      now: { now },
      feedRefresher: CountingFeedRefresher(recorder: recorder),
      feedSettleDelay: .zero
    )

    let poll = Task { await model.refresh() }
    await gate.waitUntilBlocked()
    let manual = Task { await model.refreshFromGesture() }
    await Task.yield()
    #expect(await recorder.get() == 0)

    await gate.release()
    await poll.value
    await manual.value
    #expect(await recorder.get() == 0)
    #expect(!model.fetchingFeeds)
  }

  @Test
  func cancelledManualRefreshDoesNotStartAFeedTaskAfterItsSharedRefreshCompletes() async {
    let directory = FileManager.default.temporaryDirectory
      .appendingPathComponent("localghost-home-cancel-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let gate = HomeTestGate()
    let recorder = FeedRefreshRecorder()
    let client = BlockingHomeClient(gate: gate)
    let model = HomeModel(
      client: { client },
      cache: HomeCache(fileURL: directory.appendingPathComponent("home.json")),
      feedRefresher: CountingFeedRefresher(recorder: recorder),
      feedSettleDelay: .zero
    )

    let poll = Task { await model.refresh() }
    await gate.waitUntilBlocked()
    let manual = Task { await model.refreshFromGesture() }
    manual.cancel()
    await gate.release()
    await poll.value
    await manual.value

    #expect(await recorder.get() == 0)
    #expect(!model.fetchingFeeds)
  }
}

struct HomeFeedRefresherTests {
  @Test
  func cancellationAfterAStartedFetchDoesNotPostABatch() async {
    let gate = HomeTestGate()
    let client = FeedPipelineClient(rates: [Self.source("rate")], feeds: [])
    let refresher = HomeFeedRefresher(
      fetcher: BlockingPublicFetcher(gate: gate),
      networkKind: { "wifi" }
    )
    let run = Task { await refresher.run(client: client) }
    await gate.waitUntilBlocked()

    run.cancel()
    await gate.release()
    let outcome = await run.value

    #expect(outcome.note == "cancelled")
    #expect(await client.postedRateRows() == 0)
    #expect(await client.postedFeedRows() == 0)
  }

  @Test
  func sourceCountsAndEncodedBatchesStayBounded() async {
    let rates = (0..<40).map { Self.source("rate-\($0)") }
    let feeds = (0..<70).map { Self.source("feed-\($0)") }
    let counter = PublicFetchRecorder()
    let client = FeedPipelineClient(rates: rates, feeds: feeds)
    let refresher = HomeFeedRefresher(
      fetcher: CountingPublicFetcher(recorder: counter),
      networkKind: { "wifi" }
    )

    let outcome = await refresher.run(client: client)

    #expect(await counter.get() == 96)
    #expect(await client.postedRateRows() == 32)
    #expect(await client.postedFeedRows() == 64)
    #expect(outcome.note.contains("bounded refresh"))
  }

  @Test
  func rateBodiesAreChunkedBelowTheBoxRequestLimit() async {
    let rates = (0..<5).map { Self.source("rate-\($0)") }
    let client = FeedPipelineClient(rates: rates, feeds: [])
    let refresher = HomeFeedRefresher(
      fetcher: SizedPublicFetcher(body: String(repeating: "x", count: 1_000_000)),
      networkKind: { "wifi" }
    )

    _ = await refresher.run(client: client)

    #expect(await client.rateBatchSizes() == [3, 2])
  }

  @Test
  func aFallbackThatStillExceedsTheEncodedLimitIsNeverPosted() async {
    let client = FeedPipelineClient(rates: [Self.source("rate")], feeds: [])
    let refresher = HomeFeedRefresher(
      fetcher: OversizedIDPublicFetcher(id: String(repeating: "x", count: 4_000_000)),
      networkKind: { "wifi" }
    )

    let outcome = await refresher.run(client: client)

    #expect(await client.postedRateRows() == 0)
    #expect(outcome.note.contains("bounded refresh"))
  }

  @Test
  func sourceIDsOutsideTheBoxContractAreNotFetchedOrPosted() async {
    let counter = PublicFetchRecorder()
    let client = FeedPipelineClient(rates: [Self.source(String(repeating: "x", count: 41))], feeds: [])
    let refresher = HomeFeedRefresher(
      fetcher: CountingPublicFetcher(recorder: counter),
      networkKind: { "wifi" }
    )

    _ = await refresher.run(client: client)

    #expect(await counter.get() == 0)
    #expect(await client.postedRateRows() == 0)
  }

  private static func source(_ id: String) -> HomeFetchSource {
    HomeFetchSource(id: id, name: id, url: "https://example.com/\(id)", every: 60)
  }
}

private actor HomeTestGate {
  private var open = false
  private var blocked = false
  private var waiters: [CheckedContinuation<Void, Never>] = []
  private var observers: [CheckedContinuation<Void, Never>] = []

  func block() async {
    blocked = true
    let currentObservers = observers
    observers.removeAll()
    currentObservers.forEach { $0.resume() }
    guard !open else { return }
    await withCheckedContinuation { waiters.append($0) }
  }

  func waitUntilBlocked() async {
    guard !blocked else { return }
    await withCheckedContinuation { observers.append($0) }
  }

  func release() {
    open = true
    let currentWaiters = waiters
    waiters.removeAll()
    currentWaiters.forEach { $0.resume() }
  }
}

private actor WeatherLocationRecorder {
  private var values: [HomeLocation] = []
  func append(_ location: HomeLocation) { values.append(location) }
  func get() -> [HomeLocation] { values }
}

private enum HomeTestError: Error { case unavailable }

private actor FeedRefreshRecorder {
  private var count = 0
  func increment() { count += 1 }
  func get() -> Int { count }
}

private nonisolated struct CountingFeedRefresher: HomeFeedRefreshing {
  let recorder: FeedRefreshRecorder

  func run(client _: any HomeServing) async -> HomeFeedOutcome {
    await recorder.increment()
    return HomeFeedOutcome(at: 1, feeds: 0, feedsOK: 0, rates: 0, ratesOK: 0, note: "")
  }
}

private nonisolated struct BlockingFeedRefresher: HomeFeedRefreshing {
  let gate: HomeTestGate
  let recorder: FeedRefreshRecorder

  func run(client _: any HomeServing) async -> HomeFeedOutcome {
    await recorder.increment()
    await gate.block()
    return HomeFeedOutcome(at: 1, feeds: 1, feedsOK: 1, rates: 1, ratesOK: 1, note: "")
  }
}

private nonisolated final class StaleNewsHomeClient: HomeServing, @unchecked Sendable {
  func fetchHome() async throws -> HomeSnapshot { throw HomeTestError.unavailable }
  func fetchRates() async throws -> HomeRates { throw HomeTestError.unavailable }
  func fetchFastRates() async throws -> HomeFastRates { throw HomeTestError.unavailable }
  func fetchWeather(at _: HomeLocation) async throws -> HomeWeather { throw HomeTestError.unavailable }
  func writeNewsBrief() async throws -> HomeBriefResult { throw HomeTestError.unavailable }

  func fetchNews(since _: Int64) async throws -> HomeNews {
    HomeNews(stories: [], lastFetch: 0, lastDigest: 0)
  }
}

private nonisolated final class FreshNewsHomeClient: HomeServing, @unchecked Sendable {
  private let gate: HomeTestGate
  private let lastFetch: Int64

  init(gate: HomeTestGate, lastFetch: Int64) {
    self.gate = gate
    self.lastFetch = lastFetch
  }

  func fetchHome() async throws -> HomeSnapshot {
    await gate.block()
    throw HomeTestError.unavailable
  }

  func fetchRates() async throws -> HomeRates { throw HomeTestError.unavailable }
  func fetchFastRates() async throws -> HomeFastRates { throw HomeTestError.unavailable }
  func fetchWeather(at _: HomeLocation) async throws -> HomeWeather { throw HomeTestError.unavailable }
  func writeNewsBrief() async throws -> HomeBriefResult { throw HomeTestError.unavailable }

  func fetchNews(since _: Int64) async throws -> HomeNews {
    HomeNews(stories: [], lastFetch: lastFetch, lastDigest: 0)
  }
}

private actor PublicFetchRecorder {
  private var count = 0
  func increment() { count += 1 }
  func get() -> Int { count }
}

private nonisolated struct BlockingPublicFetcher: HomePublicFetching {
  let gate: HomeTestGate

  func fetch(_ source: HomeFetchSource) async -> HomeFetchedSource {
    await gate.block()
    return HomeFetchedSource(id: source.id, status: 200, error: nil, body: "ok", tookMs: 1)
  }
}

private nonisolated struct CountingPublicFetcher: HomePublicFetching {
  let recorder: PublicFetchRecorder

  func fetch(_ source: HomeFetchSource) async -> HomeFetchedSource {
    await recorder.increment()
    return HomeFetchedSource(id: source.id, status: 200, error: nil, body: "ok", tookMs: 1)
  }
}

private nonisolated struct SizedPublicFetcher: HomePublicFetching {
  let body: String

  func fetch(_ source: HomeFetchSource) async -> HomeFetchedSource {
    HomeFetchedSource(id: source.id, status: 200, error: nil, body: body, tookMs: 1)
  }
}

private nonisolated struct OversizedIDPublicFetcher: HomePublicFetching {
  let id: String

  func fetch(_: HomeFetchSource) async -> HomeFetchedSource {
    HomeFetchedSource(id: id, status: 200, error: nil, body: "ok", tookMs: 1)
  }
}

private actor FeedPostRecorder {
  private var rateBatches: [Int] = []
  private var feedBatches: [Int] = []
  func addRates(_ count: Int) { rateBatches.append(count) }
  func addFeeds(_ count: Int) { feedBatches.append(count) }
  func rateSizes() -> [Int] { rateBatches }
  func feedSizes() -> [Int] { feedBatches }
}

private nonisolated final class FeedPipelineClient: HomeServing, @unchecked Sendable {
  private let list: HomeFetchList
  private let posts = FeedPostRecorder()

  init(rates: [HomeFetchSource], feeds: [HomeFetchSource]) {
    list = HomeFetchList(feeds: feeds, rates: rates, feedsEvery: 120)
  }

  func fetchHome() async throws -> HomeSnapshot { throw HomeTestError.unavailable }
  func fetchRates() async throws -> HomeRates { throw HomeTestError.unavailable }
  func fetchFastRates() async throws -> HomeFastRates { throw HomeTestError.unavailable }
  func fetchNews(since _: Int64) async throws -> HomeNews { throw HomeTestError.unavailable }
  func fetchWeather(at _: HomeLocation) async throws -> HomeWeather { throw HomeTestError.unavailable }
  func writeNewsBrief() async throws -> HomeBriefResult { throw HomeTestError.unavailable }
  func reportPhoneNetwork(_: String) async throws -> Bool { true }
  func fetchList() async throws -> HomeFetchList { list }

  func postFetchedRates(at _: Int64, rows: [HomeFetchedSource]) async throws -> Bool {
    await posts.addRates(rows.count)
    return true
  }

  func postFetchedNews(at _: Int64, rows: [HomeFetchedSource]) async throws -> Bool {
    await posts.addFeeds(rows.count)
    return true
  }

  func postedRateRows() async -> Int { await posts.rateSizes().reduce(0, +) }
  func postedFeedRows() async -> Int { await posts.feedSizes().reduce(0, +) }
  func rateBatchSizes() async -> [Int] { await posts.rateSizes() }
}

private nonisolated final class BlockingHomeClient: HomeServing, @unchecked Sendable {
  private let gate: HomeTestGate
  private let locations = WeatherLocationRecorder()

  init(gate: HomeTestGate) { self.gate = gate }

  func fetchHome() async throws -> HomeSnapshot {
    await gate.block()
    throw HomeTestError.unavailable
  }

  func fetchRates() async throws -> HomeRates { throw HomeTestError.unavailable }
  func fetchFastRates() async throws -> HomeFastRates { throw HomeTestError.unavailable }
  func fetchNews(since _: Int64) async throws -> HomeNews { throw HomeTestError.unavailable }

  func fetchWeather(at location: HomeLocation) async throws -> HomeWeather {
    await locations.append(location)
    return HomeWeather(ok: true, noGeo: nil, text: nil, forecast: nil, table: nil)
  }

  func writeNewsBrief() async throws -> HomeBriefResult { throw HomeTestError.unavailable }
  func weatherLocations() async -> [HomeLocation] { await locations.get() }
}
