import java.util.Properties
import org.gradle.api.artifacts.VersionCatalogsExtension
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
}

// --- build provenance: read git + source manifest at configure time ---
// Uses providers.exec (configuration-cache safe); falls back to "" if git isn't available.
fun git(vararg args: String): String = try {
    providers.exec {
        commandLine("git", *args)
        isIgnoreExitValue = true
    }.standardOutput.asText.get().trim()
} catch (e: Exception) { "" }

val gitCommit = git("rev-parse", "HEAD").ifEmpty { "unknown" }
val gitCommitShort = git("rev-parse", "--short", "HEAD").ifEmpty { "unknown" }
val gitTreeClean = git("status", "--porcelain").isEmpty()
// the build time stamped in: the commit's own time when the tree is exactly the commit (so a
// release built again from its tag is the same APK), the clock's otherwise
val buildTimeUtc: String = DateTimeFormatter.ISO_INSTANT
    .withZone(ZoneOffset.UTC)
    .format(git("log", "-1", "--format=%ct").toLongOrNull()?.takeIf { gitTreeClean }?.let { Instant.ofEpochSecond(it) } ?: Instant.now())
val manifestRoot = rootProject.file("MANIFEST.root")
    .let { if (it.exists()) it.readText().trim() else "" }

// Build-environment provenance. Two tiers:
//  - STABLE values go into BuildConfig (below): they're the same on any machine that uses the
//    pinned toolchain, so they don't change the APK bytes. Major JVM (e.g. "21"), OS name,
//    and the tool versions pinned in libs.versions.toml.
//  - FULL values (exact JDK patch, kernel string) go into ghost/build-env.txt via the
//    writeBuildEnv task, NOT into the APK, so reproducibility is preserved.
val jvmMajor: String = System.getProperty("java.version").orEmpty().substringBefore(".")
val osName: String = System.getProperty("os.name").orEmpty()

// THE RELEASE THE APP BELONGS TO. One number for the phone and the box: versionName here, and the
// name beside it from server/tools/release.names ("0.0.2 wisp"), the same file the box reads at
// an exact tag, so the phone's VERIFY BUILD and the box's SETTINGS › SERVER say the same thing.
// tools/cut_release.sh refuses an APK whose versionName is not the release being cut.
val appVersion = "0.0.6"
val appVersionCode = 6
val releaseName: String = rootProject.file("../../server/tools/release.names").takeIf { it.isFile }
    ?.readLines()?.map { it.trim() }?.firstOrNull { it.startsWith("$appVersion ") }
    ?.substringAfter(' ')?.trim().orEmpty()
val gradleVersion: String = gradle.gradleVersion
// Read AGP/Kotlin versions straight from the version catalog [versions] table.
val catalog = extensions.getByType<VersionCatalogsExtension>().named("libs")
val agpVersion: String = catalog.findVersion("agp").map { it.toString() }.orElse("unknown")
val kotlinVersion: String = catalog.findVersion("kotlin").map { it.toString() }.orElse("unknown")

// THE PHONE'S MODEL (llama.cpp, src/main/cpp). The phone builds the same llama.cpp source the box
// builds from: the tarball on the LocalGhost mirror, pinned by SHA-256 in CMakeLists.txt. Until the
// pin is set the APK builds without the runtime and the app says so (MODELS in the menu), rather
// than breaking every build. Set it with app/android/tools/pin_llama.sh. The tarball is fetched from
// the mirror at configure time, or taken from a local copy: -PllamaTarball=/path/to/<tarball>
// (on the box: /opt/localghost/llama.cpp.mirror-dl/), checked against the pin either way.
val llamaCmake = file("src/main/cpp/CMakeLists.txt")
fun cmakeVar(name: String): String = if (llamaCmake.exists())
    Regex("""set\($name\s+"([^"]*)"""").find(llamaCmake.readText())?.groupValues?.get(1).orEmpty() else ""
val llamaSha: String = cmakeVar("LLAMA_CPP_SHA256")
val llamaPin: String = cmakeVar("LLAMA_CPP_TAG") + "-" + cmakeVar("LLAMA_CPP_COMMIT")
val buildPhoneModel = Regex("^[0-9a-f]{64}$").matches(llamaSha)
// Where the tarball comes from, first found: -PllamaTarball; llamaTarball= in local.properties (a
// Windows build machine: copy the tarball over from the box once and name it there, forward
// slashes, e.g. llamaTarball=C:/Users/you/llama.cpp-v0.5.0-7fe450e.tar.gz); on the box itself,
// the copy setup_llama.sh verified and kept. CMake checks it against the pin whichever it is, so a
// build needs no network at all. Nothing found: CMake fetches it from the mirror.
val llamaTarball: String = (findProperty("llamaTarball") as String?).orEmpty().ifEmpty {
    rootProject.file("local.properties").takeIf { it.isFile }?.let { f ->
        Properties().apply { f.inputStream().use { load(it) } }.getProperty("llamaTarball")?.trim()
    }.orEmpty()
}.ifEmpty {
    file("/opt/localghost/llama.cpp.mirror-dl/" + cmakeVar("LLAMA_CPP_TARBALL"))
        .takeIf { buildPhoneModel && cmakeVar("LLAMA_CPP_TARBALL").isNotEmpty() && it.isFile }?.path.orEmpty()
}
val llamaArch: String = (findProperty("llamaArch") as String?) ?: "armv8.2-a+dotprod+fp16"

android {
    val localProps = Properties().apply {
        rootProject.file("local.properties").takeIf { it.exists() }
            ?.inputStream()?.use { load(it) }
    }

    namespace = "com.localghost.app"
    compileSdk {
        version = release(37)
    }
    defaultConfig {
        applicationId = "com.localghost.app"
        minSdk = 35
        targetSdk = 36
        versionCode = appVersionCode
        versionName = appVersion // the release the app belongs to (server/tools/release.names)
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
        buildConfigField("String", "GITHUB_REPO", "\"https://github.com/LocalGhostDao/localghost\"")
        // the release's name (server/tools/release.names), shown beside the version
        buildConfigField("String", "RELEASE_NAME", "\"$releaseName\"")
        // Stable build-env (same on any machine using the pinned toolchain; safe in the APK).
        buildConfigField("String", "BUILD_JVM_MAJOR", "\"$jvmMajor\"")
        buildConfigField("String", "BUILD_OS_NAME", "\"$osName\"")
        buildConfigField("String", "BUILD_GRADLE_VERSION", "\"$gradleVersion\"")
        buildConfigField("String", "BUILD_AGP_VERSION", "\"$agpVersion\"")
        buildConfigField("String", "BUILD_KOTLIN_VERSION", "\"$kotlinVersion\"")
        // whether this APK carries the phone's model runtime, and which llama.cpp
        buildConfigField("boolean", "HAS_PHONE_MODEL", "$buildPhoneModel")
        buildConfigField("String", "LLAMA_CPP_COMMIT", "\"$llamaPin\"")
        if (buildPhoneModel) {
            ndk { abiFilters += "arm64-v8a" }
            externalNativeBuild {
                cmake {
                    arguments += listOf("-DANDROID_STL=c++_static", "-DLG_CPU_ARCH=$llamaArch") +
                        (if (llamaTarball.isNotEmpty()) listOf("-DLLAMA_CPP_TARBALL_PATH=$llamaTarball") else emptyList())
                    cppFlags += "-std=c++17"
                }
            }
        }
    }
    if (buildPhoneModel) {
        externalNativeBuild {
            cmake {
                path = llamaCmake
                version = "3.22.1"
            }
        }
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
    // The NDK that builds the phone model's runtime, pinned like build-tools so Windows and Linux
    // build with the same compiler (the first Windows build of llama.cpp v0.5.0 used this one).
    // Install it with the SDK Manager or: sdkmanager "ndk;28.2.13676358" (see BUILDING.md).
    ndkVersion = "28.2.13676358"
}
dependencies {
    implementation("androidx.health.connect:connect-client:1.1.0-alpha07")
    // Media3 , the ONE dependency the video feature earns: a custom DataSource turns the player's
    // byte requests into in-process calls on our authenticated channel, deleting the loopback
    // proxy and its entire attack surface. androidx = the same trust root as Compose.
    implementation("androidx.media3:media3-exoplayer:1.4.1")
    implementation("androidx.media3:media3-ui:1.4.1")
    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material3.adaptive.navigation.suite)
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.work.runtime.ktx)
    implementation(libs.androidx.camera.core)
    implementation(libs.androidx.camera.camera2)
    implementation(libs.androidx.camera.lifecycle)
    implementation(libs.androidx.camera.view)
    testImplementation(libs.junit)
    androidTestImplementation(platform(libs.androidx.compose.bom))
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    androidTestImplementation(libs.androidx.espresso.core)
    androidTestImplementation(libs.androidx.junit)
    debugImplementation(libs.androidx.compose.ui.test.manifest)
    debugImplementation(libs.androidx.compose.ui.tooling)
}

// Writes the FULL build environment to ghost/build-env.txt so a verifier knows exactly what to
// install to reproduce the build. Deliberately NOT in BuildConfig: the exact JDK patch and kernel
// string vary between machines and would change the APK bytes. Run by tools/release.sh before the
// build; the file is committed and signed alongside the source manifest.
tasks.register("writeBuildEnv") {
    val out = rootProject.file("ghost/build-env.txt")
    val commit = gitCommit
    notCompatibleWithConfigurationCache("writes a provenance file at execution time")
    doLast {
        out.parentFile.mkdirs()
        out.writeText(buildString {
            appendLine("# LocalGhost App Build Environment")
            appendLine("# Build: $commit")
            appendLine("# Written: ${DateTimeFormatter.ISO_INSTANT.withZone(ZoneOffset.UTC).format(Instant.now())}")
            appendLine()
            appendLine("jdk.version      ${System.getProperty("java.version")}")
            appendLine("jdk.vendor       ${System.getProperty("java.vendor")}")
            appendLine("jdk.home         ${System.getProperty("java.home")}")
            appendLine("os.name          ${System.getProperty("os.name")}")
            appendLine("os.version       ${System.getProperty("os.version")}")
            appendLine("os.arch          ${System.getProperty("os.arch")}")
            appendLine("gradle.version   ${gradle.gradleVersion}")
            appendLine("agp.version      $agpVersion")
            appendLine("kotlin.version   $kotlinVersion")
            appendLine("compileSdk       37")
            appendLine("targetSdk        36")
            appendLine("minSdk           35")
            appendLine("buildTools       36.0.0")
            val cmake = rootProject.file("app/src/main/cpp/CMakeLists.txt")
            if (cmake.exists()) {
                val txt = cmake.readText()
                val tag = Regex("""LLAMA_CPP_TAG[^"]*"([^"]+)"""").find(txt)?.groupValues?.get(1) ?: "unknown"
                val commit = Regex("""LLAMA_CPP_COMMIT[^"]*"([^"]+)"""").find(txt)?.groupValues?.get(1) ?: "unknown"
                val sha = Regex("""LLAMA_CPP_SHA256[^"]*"([^"]+)"""").find(txt)?.groupValues?.get(1) ?: "unset"
                appendLine("llama.cpp.tag    $tag")
                appendLine("llama.cpp.commit $commit")
                appendLine("llama.cpp.sha256 $sha  (the mirror tarball the phone builds from)")
            }
        })
        println("wrote ${out.relativeTo(rootProject.projectDir)}")
    }
}
