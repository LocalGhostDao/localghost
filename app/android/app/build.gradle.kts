import java.util.Properties
import java.io.ByteArrayOutputStream

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

// --- build provenance: read git + source manifest at configure time ---
fun git(vararg args: String): String = try {
    val out = ByteArrayOutputStream()
    exec {
        commandLine("git", *args)
        standardOutput = out
        isIgnoreExitValue = true
    }
    out.toString().trim()
} catch (e: Exception) { "" }

val gitCommit = git("rev-parse", "HEAD").ifEmpty { "unknown" }
val gitCommitShort = git("rev-parse", "--short", "HEAD").ifEmpty { "unknown" }
val gitTreeClean = git("status", "--porcelain").isEmpty()
val buildTimeUtc: String = java.time.format.DateTimeFormatter.ISO_INSTANT
    .withZone(java.time.ZoneOffset.UTC)
    .format(java.time.Instant.now())
val manifestRoot = rootProject.file("MANIFEST.root")
    .let { if (it.exists()) it.readText().trim() else "" }

android {
    val localProps = Properties().apply {
        rootProject.file("local.properties").takeIf { it.exists() }
            ?.inputStream()?.use { load(it) }
    }

    namespace = "com.localghost.app"
    compileSdk {
        version = release(36) {
            minorApiLevel = 1
        }
    }
    defaultConfig {
        applicationId = "com.localghost.app"
        minSdk = 35
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"

        // Dev convenience only. The PUBLIC release build leaves these EMPTY: the app reads the
        // box URL + device token from its own encrypted storage (written during setup). An empty
        // value at runtime = unconfigured = show setup. This keeps the release APK reproducible
        // (no machine-specific data baked in).
        buildConfigField("String", "NAS_BASE_URL",
            "\"${localProps.getProperty("NAS_BASE_URL", "")}\"")
        buildConfigField("String", "DEVICE_TOKEN",
            "\"${localProps.getProperty("DEVICE_TOKEN", "")}\"")

        // Build provenance — surfaced by the in-app VERIFY BUILD screen.
        buildConfigField("String", "GIT_COMMIT", "\"$gitCommit\"")
        buildConfigField("String", "GIT_COMMIT_SHORT", "\"$gitCommitShort\"")
        buildConfigField("boolean", "GIT_TREE_CLEAN", "$gitTreeClean")
        buildConfigField("String", "BUILD_TIME_UTC", "\"$buildTimeUtc\"")
        buildConfigField("String", "MANIFEST_ROOT", "\"$manifestRoot\"")
        buildConfigField("String", "GITHUB_REPO", "\"https://github.com/localghost-ai/localghost-app\"")
    }
    buildTypes {
        release {
            optimization {
                enable = false
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }
    buildFeatures {
        compose = true
        buildConfig = true
    }
    // Keep dependency metadata out of the APK so it doesn't introduce per-build variance.
    dependenciesInfo {
        includeInApk = false
        includeInBundle = false
    }
    buildToolsVersion = "36.0.0"
}
dependencies {
    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material3.adaptive.navigation.suite)
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation("androidx.work:work-runtime-ktx:2.10.1")
    testImplementation(libs.junit)
    androidTestImplementation(platform(libs.androidx.compose.bom))
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    androidTestImplementation(libs.androidx.espresso.core)
    androidTestImplementation(libs.androidx.junit)
    debugImplementation(libs.androidx.compose.ui.test.manifest)
    debugImplementation(libs.androidx.compose.ui.tooling)
}
