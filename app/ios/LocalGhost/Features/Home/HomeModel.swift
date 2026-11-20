import Foundation
import Observation

@MainActor
@Observable
final class HomeModel {
  private(set) var snapshot: HomeSnapshot?
  private(set) var rates: HomeRates?
  private(set) var fastRates: HomeFastRates?
  private(set) var news: HomeNews?
  private(set) var forYou: HomeForYou?
  private(set) var weather: HomeWeather?
  private(set) var locationSupplied = false
  private(set) var weatherAttempted = false
  private(set) var weatherErrorMessage: String?
  private(set) var now: Date
  private(set) var refreshing = false
  private(set) var fetchingFeeds = false
  private(set) var feedMessage: String?
  private(set) var writingBrief = false
  private(set) var errorMessage: String?
  private(set) var briefMessage: String?

  @ObservationIgnored private let clientProvider: @MainActor () throws -> any HomeServing
  @ObservationIgnored private let locationProvider: @MainActor () -> HomeLocation?
  @ObservationIgnored private let cache: HomeCache
  @ObservationIgnored private let clock: @MainActor () -> Date
  @ObservationIgnored private let pollEvery: Duration
  @ObservationIgnored private let fastEvery: Duration
  @ObservationIgnored private let clockEvery: Duration
  @ObservationIgnored private let feedRefresher: any HomeFeedRefreshing
  @ObservationIgnored private let feedSettleDelay: Duration
  @ObservationIgnored private var loadedCache = false
  @ObservationIgnored private var refreshingFast = false
  @ObservationIgnored private var refreshAgain = false
  @ObservationIgnored private var refreshWaiters: [CheckedContinuation<Void, Never>] = []
  @ObservationIgnored private var feedTask: Task<Void, Never>?

  init(
    client: @escaping @MainActor () throws -> any HomeServing,
    location: @escaping @MainActor () -> HomeLocation? = { nil },
    cache: HomeCache = HomeCache(),
    now: @escaping @MainActor () -> Date = { Date() },
    pollEvery: Duration = .seconds(60),
    fastEvery: Duration = .seconds(5),
    clockEvery: Duration = .seconds(1),
    feedRefresher: any HomeFeedRefreshing = HomeFeedRefresher(),
    feedSettleDelay: Duration = .seconds(35)
  ) {
    clientProvider = client
    locationProvider = location
    self.cache = cache
    clock = now
    self.now = now()
    self.pollEvery = pollEvery
    self.fastEvery = fastEvery
    self.clockEvery = clockEvery
    self.feedRefresher = feedRefresher
    self.feedSettleDelay = feedSettleDelay
  }

  /// Runs for exactly as long as the view's `.task` does. Cancellation stops all three cadences.
  func run() async {
    defer {
      feedTask?.cancel()
      feedTask = nil
      fetchingFeeds = false
    }
    await loadCacheIfNeeded()
    await withTaskGroup(of: Void.self) { group in
      group.addTask { await self.pollLoop() }
      group.addTask { await self.fastLoop() }
      group.addTask { await self.clockLoop() }
      await group.waitForAll()
    }
  }

  /// Android's pull-to-refresh contract: read the box immediately, then fetch public sources on
  /// this phone only when the box's feed mark is at least half an hour old.
  func refreshFromGesture() async {
    await refresh()
    // A cancelled pull-to-refresh must not outlive Home by spawning a new unstructured feed task.
    guard !Task.isCancelled else { return }
    let lastFetch = news?.lastFetch ?? 0
    let age = Int64(clock().timeIntervalSince1970) - lastFetch
    guard lastFetch <= 0 || age >= 30 * 60 else { return }
    startFeedRefresh()
  }

  func refresh() async {
    guard !refreshing else {
      refreshAgain = true
      await withCheckedContinuation { refreshWaiters.append($0) }
      return
    }
    refreshing = true
    defer {
      refreshing = false
      refreshAgain = false
      let waiters = refreshWaiters
      refreshWaiters.removeAll()
      waiters.forEach { $0.resume() }
    }

    repeat {
      refreshAgain = false
      await refreshOnce()
    } while refreshAgain && !Task.isCancelled
  }

  private func refreshOnce() async {
    let client: any HomeServing
    do {
      client = try clientProvider()
    } catch {
      errorMessage = error.localizedDescription
      return
    }

    let location = locationProvider()
    locationSupplied = location != nil
    if location == nil {
      weatherAttempted = false
      weatherErrorMessage = nil
    }
    let since = Int64(clock().timeIntervalSince1970) - 86_400

    async let homeResult = capture { try await client.fetchHome() }
    async let ratesResult = capture { try await client.fetchRates() }
    async let newsResult = capture { try await client.fetchNews(since: since) }
    async let weatherResult = captureWeather(client: client, location: location)

    let (home, currentRates, currentNews, currentWeather) = await (
      homeResult, ratesResult, newsResult, weatherResult
    )
    guard !Task.isCancelled else { return }
    var answered = false

    if case .success(let value) = home {
      snapshot = value
      if let personal = value.forYou { forYou = personal }
      answered = true
    }
    if case .success(let value) = currentRates {
      rates = value
      answered = true
    }
    if case .success(let value) = currentNews {
      news = value
      answered = true
    }
    if let currentWeather {
      weatherAttempted = true
      switch currentWeather {
      case .success(let value):
        weather = value
        weatherErrorMessage = nil
      case .failure(let error):
        weatherErrorMessage = error is CancellationError ? nil : error.localizedDescription
      }
    }

    errorMessage = answered ? nil : home.failureDescription
      ?? currentRates.failureDescription
      ?? currentNews.failureDescription
      ?? "The box did not answer."
    await saveCache()
  }

  func writeBrief() async {
    guard !writingBrief else { return }
    writingBrief = true
    briefMessage = nil
    defer { writingBrief = false }

    do {
      let result = try await clientProvider().writeNewsBrief()
      guard result.written else {
        briefMessage = result.why.isEmpty ? "The box did not write the brief." : result.why
        return
      }
      news = updatedNews(with: result)
      await saveCache()
    } catch is CancellationError {
      return
    } catch {
      briefMessage = error.localizedDescription
    }
  }

  private func pollLoop() async {
    while !Task.isCancelled {
      await refresh()
      do {
        try await Task.sleep(for: pollEvery)
      } catch {
        return
      }
    }
  }

  private func fastLoop() async {
    while !Task.isCancelled {
      await refreshFast()
      do {
        try await Task.sleep(for: fastEvery)
      } catch {
        return
      }
    }
  }

  private func clockLoop() async {
    while !Task.isCancelled {
      now = clock()
      do {
        try await Task.sleep(for: clockEvery)
      } catch {
        return
      }
    }
  }

  private func refreshFast() async {
    guard !refreshingFast else { return }
    refreshingFast = true
    defer { refreshingFast = false }
    do {
      fastRates = try await clientProvider().fetchFastRates()
    } catch is CancellationError {
      return
    } catch {
      // The minute snapshot remains truthful; the five-second lane is opportunistic.
    }
  }

  private func startFeedRefresh() {
    guard !fetchingFeeds else { return }
    fetchingFeeds = true
    feedMessage = nil
    feedTask = Task { [weak self] in
      guard let self else { return }
      let client: any HomeServing
      do {
        client = try self.clientProvider()
      } catch {
        self.feedMessage = error.localizedDescription
        self.fetchingFeeds = false
        self.feedTask = nil
        return
      }

      let outcome = await self.feedRefresher.run(client: client)
      guard !Task.isCancelled else { return }
      self.feedMessage = outcome.note.isEmpty ? nil : outcome.note
      do {
        try await Task.sleep(for: self.feedSettleDelay)
      } catch {
        return
      }
      await self.refresh()
      guard !Task.isCancelled else { return }
      self.fetchingFeeds = false
      self.feedTask = nil
    }
  }

  private func loadCacheIfNeeded() async {
    guard !loadedCache else { return }
    loadedCache = true
    guard let state = await cache.load() else { return }
    snapshot = state.snapshot
    rates = state.rates
    news = state.news
    weather = state.weather
  }

  private func saveCache() async {
    do {
      try await cache.save(snapshot: snapshot, rates: rates, news: news, weather: weather)
    } catch {
      // Cache failure must not turn a successful box read into a failed Home screen.
    }
  }

  private func updatedNews(with brief: HomeBriefResult) -> HomeNews {
    let current = news ?? HomeNews(
      stories: snapshot?.top.map(Self.newsStory(from:)) ?? [],
      lastFetch: 0,
      lastDigest: 0,
      brief: "",
      briefAt: 0,
      briefStories: []
    )
    return HomeNews(
      stories: current.stories,
      lastFetch: current.lastFetch,
      lastDigest: current.lastDigest,
      brief: brief.brief,
      briefAt: brief.briefAt,
      briefStories: brief.briefStories
    )
  }

  private static func newsStory(from story: HomeStory) -> HomeNewsStory {
    HomeNewsStory(
      id: story.id,
      title: story.title,
      summary: story.lead,
      sources: story.sources,
      firstSeen: story.lastSeen,
      lastSeen: story.lastSeen,
      items: story.outlets.map {
        HomeNewsItem(feed: "", outlet: $0, title: "", link: "", summary: "", published: 0)
      }
    )
  }
}

private nonisolated func capture<Value: Sendable>(
  _ operation: @escaping @Sendable () async throws -> Value
) async -> Result<Value, Error> {
  do {
    return .success(try await operation())
  } catch {
    return .failure(error)
  }
}

private nonisolated func captureWeather(
  client: any HomeServing,
  location: HomeLocation?
) async -> Result<HomeWeather, Error>? {
  guard let location else { return nil }
  return await capture { try await client.fetchWeather(at: location) }
}

private extension Result where Failure == Error {
  var failureDescription: String? {
    guard case .failure(let error) = self else { return nil }
    if error is CancellationError { return nil }
    return error.localizedDescription
  }
}
