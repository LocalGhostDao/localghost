import Foundation

nonisolated struct HomeCachedState: Codable, Sendable, Equatable {
  let snapshot: HomeSnapshot?
  let rates: HomeRates?
  let news: HomeNews?
  let weather: HomeWeather?

  init(snapshot: HomeSnapshot?, rates: HomeRates?, news: HomeNews?, weather: HomeWeather?) {
    self.snapshot = snapshot?.publicCacheCopy
    self.rates = rates
    self.news = news
    self.weather = weather
  }
}

/// Where Home's cache is kept: in Caches, which iOS may empty and no backup carries. Forgetting
/// the box removes the file (SignedInLeftovers).
nonisolated func homeCacheFileURL() -> URL {
  let root = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
    ?? URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
  return root
    .appendingPathComponent("LocalGhost", isDirectory: true)
    .appendingPathComponent("home.json", isDirectory: false)
}

/// The public feed data and last forecast used for an immediate Home render. Personal
/// recommendations, calendar rows and coordinates never cross this actor's encoder.
actor HomeCache {
  private var invalidated = false
  private let fileURL: URL
  private let decoder = JSONDecoder()
  private let encoder = JSONEncoder()

  init(fileURL: URL) {
    self.fileURL = fileURL
  }

  init() {
    fileURL = homeCacheFileURL()
  }

  func load() -> HomeCachedState? {
    guard !invalidated, let data = try? Data(contentsOf: fileURL),
          let decoded = try? decoder.decode(HomeCachedState.self, from: data)
    else {
      return nil
    }
    // Re-sanitise an older cache written before the privacy boundary was introduced.
    let sanitised = HomeCachedState(
      snapshot: decoded.snapshot,
      rates: decoded.rates,
      news: decoded.news,
      weather: decoded.weather
    )
    if sanitised != decoded { try? write(sanitised) }
    return sanitised
  }

  func save(
    snapshot: HomeSnapshot?,
    rates: HomeRates?,
    news: HomeNews?,
    weather: HomeWeather?
  ) throws {
    try write(HomeCachedState(snapshot: snapshot, rates: rates, news: news, weather: weather))
  }

  /// Serialized with writes: pending saves finish before deletion or are discarded afterwards.
  /// This instance stays retired so late network responses cannot recreate the old box's file.
  func invalidate() throws {
    invalidated = true
    do {
      try FileManager.default.removeItem(at: fileURL)
    } catch CocoaError.fileNoSuchFile {
      // Already absent. Other deletion failures must be reported to the caller.
    }
  }

  private func write(_ payload: HomeCachedState) throws {
    guard !invalidated else { return }
    let directory = fileURL.deletingLastPathComponent()
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    let data = try encoder.encode(payload)
    try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUnlessOpen])
  }
}
