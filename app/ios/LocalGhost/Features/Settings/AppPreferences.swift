import Foundation
import Observation

/// Phone preferences only. No credentials, account data or unused sync switches belong here.
@MainActor
@Observable
final class AppPreferences {
  private let defaults: UserDefaults

  var weatherLocation: Bool { didSet { defaults.set(weatherLocation, forKey: "settings.weather-location") } }
  var fetchPublicFeeds: Bool { didSet { defaults.set(fetchPublicFeeds, forKey: "settings.fetch-public-feeds") } }
  var reduceMotion: Bool { didSet { defaults.set(reduceMotion, forKey: "settings.reduce-motion") } }

  init(defaults: UserDefaults = .standard) {
    self.defaults = defaults
    weatherLocation = defaults.object(forKey: "settings.weather-location") as? Bool ?? true
    fetchPublicFeeds = defaults.object(forKey: "settings.fetch-public-feeds") as? Bool ?? true
    reduceMotion = defaults.object(forKey: "settings.reduce-motion") as? Bool ?? false
  }
}
