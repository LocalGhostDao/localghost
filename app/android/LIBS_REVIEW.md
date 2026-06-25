# libs.versions.toml review + WorkManager

## Anything you shouldn't use?
No. Everything in the catalog is current (June 2026) and fine:
- agp 9.2.1, kotlin 2.4.0, composeBom 2026.06.00 are all current.
- The junit / androidx-junit / espresso entries are the standard test scaffold. Keep them, the
  new tests use junit.

Two small notes, neither is a problem:
1. The [plugins] block has android-application and kotlin-compose but no kotlin-android
   (org.jetbrains.kotlin.android). With AGP 9 + Kotlin 2.x this can be fine if the build resolves
   Kotlin through the Compose plugin, and you said it builds and runs, so leave it. Only revisit
   if a clean build ever complains about the Kotlin Android plugin.
2. WorkManager is the one dependency pinned as a raw string in build.gradle.kts instead of the
   catalog. Move it in for consistency and reproducibility (below).

## Add WorkManager to the catalog
In `gradle/libs.versions.toml`:

```toml
[versions]
# ...
workRuntime = "2.11.2"

[libraries]
# ...
androidx-work-runtime-ktx = { group = "androidx.work", name = "work-runtime-ktx", version.ref = "workRuntime" }
```

Then in `app/build.gradle.kts` replace:
```kotlin
implementation("androidx.work:work-runtime-ktx:2.11.2")
```
with:
```kotlin
implementation(libs.androidx.work.runtime.ktx)
```

Now every dependency goes through the catalog and all versions live in one committed file, which
is what the reproducible-build verification depends on.
