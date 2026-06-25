# Verifiability, Gradle wiring (DONE)

The verify wiring is already merged into `app/build.gradle.kts`. It:
- reads git at configure time (gitCommit, gitCommitShort, gitTreeClean, buildTimeUtc),
- reads `MANIFEST.root` (written by tools/sign_source.sh),
- injects BuildConfig: GIT_COMMIT, GIT_COMMIT_SHORT, GIT_TREE_CLEAN, BUILD_TIME_UTC,
  MANIFEST_ROOT, GITHUB_REPO,
- sets dependenciesInfo includeInApk/includeInBundle = false (reproducibility hygiene).

NAS_BASE_URL / DEVICE_TOKEN stay as-is (dev convenience from local.properties). The PUBLIC
release leaves them EMPTY in local.properties, the app reads box URL + token from its own
encrypted storage, written during setup. Empty at runtime = unconfigured = show setup.

Set GITHUB_REPO to the real public repo path (currently localghost-ai/localghost-app).

Build flow: see VERIFY.md "Releaser steps".
