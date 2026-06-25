# Moving WorkManager into the version catalog (optional tidy-up)

Why work-runtime-ktx looks different from the other dependencies: every other dependency uses
`libs.xxx`, which resolves through the version catalog at `gradle/libs.versions.toml`. WorkManager
was added as a raw string literal instead, so its version is pinned in build.gradle.kts rather
than in the catalog with everything else. It builds fine, but for reproducibility the version
belongs in the committed catalog where all versions live together.

To move it, in `gradle/libs.versions.toml`:

```toml
[versions]
# ...existing...
workRuntime = "2.11.2"

[libraries]
# ...existing...
androidx-work-runtime-ktx = { group = "androidx.work", name = "work-runtime-ktx", version.ref = "workRuntime" }
```

Then in `app/build.gradle.kts`, replace:
```kotlin
implementation("androidx.work:work-runtime-ktx:2.11.2")
```
with:
```kotlin
implementation(libs.androidx.work.runtime.ktx)
```

Now it matches the others and the version is visible alongside the rest in the catalog. No
behaviour change.
