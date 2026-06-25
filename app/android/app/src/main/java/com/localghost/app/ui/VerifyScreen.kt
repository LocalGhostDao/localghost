package com.localghost.app.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.widget.Toast
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.localghost.app.BuildConfig
import com.localghost.app.ui.theme.*
import java.security.MessageDigest

@Composable
fun VerifyScreen() {
    val ctx = LocalContext.current
    val signingFp = remember { signingCertSha256(ctx) }
    val repo = BuildConfig.GITHUB_REPO
    val commit = BuildConfig.GIT_COMMIT
    val commitUrl = "$repo/tree/$commit"
    val manifestUrl = "$repo/blob/$commit/ghost/source-manifest.txt"
    val releaseUrl = "$repo/releases/tag/v${BuildConfig.VERSION_NAME}"

    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())
        .padding(20.dp).padding(bottom = 24.dp)) {

        SectionLabel("VERIFY THIS BUILD")
        Spacer(Modifier.height(8.dp))
        Text("This app is open source. You don't have to trust us. You can check that the copy " +
             "on your phone was built from this exact source, by us, with nothing changed. " +
             "Everything you need is below; follow the steps on your PC.",
             color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)

        Spacer(Modifier.height(20.dp))
        SectionLabel("WHAT THIS BUILD CLAIMS")
        Spacer(Modifier.height(8.dp))

        Field(ctx, "built (UTC)", BuildConfig.BUILD_TIME_UTC)
        Field(ctx, "version", "${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})")
        Field(ctx, "source commit", commit, mono = true)
        Field(ctx, "working tree at build",
            if (BuildConfig.GIT_TREE_CLEAN) "clean, matches the commit exactly"
            else "DIRTY, had uncommitted changes, does NOT match the commit",
            warn = !BuildConfig.GIT_TREE_CLEAN)
        Field(ctx, "source manifest root", BuildConfig.MANIFEST_ROOT.ifEmpty { "(not stamped)" }, mono = true)
        Field(ctx, "signing cert SHA-256", signingFp, mono = true)

        Spacer(Modifier.height(16.dp))
        LinkButton("OPEN SOURCE AT THIS COMMIT", commitUrl, ctx)
        Spacer(Modifier.height(8.dp))
        LinkButton("OPEN SIGNED SOURCE MANIFEST", manifestUrl, ctx)
        Spacer(Modifier.height(8.dp))
        LinkButton("OPEN THIS RELEASE", releaseUrl, ctx)
        Spacer(Modifier.height(8.dp))
        LinkButton("OUR PGP PUBLIC KEY", "https://www.localghost.ai/.well-known/pgp-key.asc", ctx)

        Spacer(Modifier.height(24.dp))
        SectionLabel("VERIFY ON YOUR PC")
        Spacer(Modifier.height(8.dp))
        Text("Run these in order. Each command block is tappable to copy.",
             color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
        Spacer(Modifier.height(12.dp))

        StepText("1", "Get the exact source and confirm the tree is clean.")
        Cmd(ctx, "git clone $repo\n" +
                 "cd ${repoName(repo)}\n" +
                 "git checkout $commit\n" +
                 "git status --porcelain   # must print nothing")

        StepText("2", "Confirm the source matches our signed manifest (signed by info@localghost.ai). " +
                       "Verifies the GPG signature, then re-hashes every file.")
        Cmd(ctx, "gpg --verify ghost/source-manifest.txt.asc ghost/source-manifest.txt\n" +
                 "tools/verify_source.sh")
        Text("The manifest root printed here must equal \"source manifest root\" above.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
             modifier = Modifier.padding(start = 4.dp, top = 2.dp))

        StepText("3", "Rebuild from that source. A reproducible build yields the same APK.")
        Cmd(ctx, "./gradlew assembleRelease\n" +
                 "sha256sum app/build/outputs/apk/release/app-release.apk")

        StepText("4", "Confirm the APK on your phone is byte-identical to what you built and to " +
                       "the hash we publish in the release.")
        Cmd(ctx, "adb shell pm path com.localghost.app\n" +
                 "adb pull <the printed path> phone.apk\n" +
                 "sha256sum phone.apk")

        StepText("5", "Confirm the APK is signed by our key (not a fork). The fingerprint must " +
                       "equal \"signing cert SHA-256\" above and the one in the release notes.")
        Cmd(ctx, "apksigner verify --print-certs phone.apk")

        StepText("6", "Confirm our GPG identity vouches for this exact binary, the same key " +
                       "(info@localghost.ai) that signs our website and source. Download the " +
                       ".asc from the release next to the APK.")
        Cmd(ctx, "gpg --verify app-release.apk.asc app-release.apk")

        Spacer(Modifier.height(16.dp))
        Text("If steps 2–5 all match, the app you are running is exactly this source, compiled " +
             "and signed by us, with no changes. That is the whole claim, and you just checked " +
             "it yourself.",
             color = GhostText, style = MaterialTheme.typography.bodyMedium)

        Spacer(Modifier.height(16.dp))
        Text("The only cloud is you. Trust is not asked for. It is checked.",
            color = TerminalDim, style = MaterialTheme.typography.labelMedium,
            textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())
    }
}

@Composable
private fun Field(ctx: Context, label: String, value: String, mono: Boolean = false, warn: Boolean = false) {
    Column(Modifier.fillMaxWidth().clickable { copy(ctx, label, value) }.padding(vertical = 6.dp)) {
        Text(label, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(2.dp))
        Text(value, color = if (warn) Warning else GhostText,
            style = if (mono) MaterialTheme.typography.labelMedium else MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun StepText(n: String, text: String) {
    Row(Modifier.fillMaxWidth().padding(top = 14.dp, bottom = 6.dp)) {
        Text("$n ", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
        Text(text, color = GhostText, style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun Cmd(ctx: Context, text: String) {
    Box(Modifier.fillMaxWidth()
        .background(Void)
        .clickable { copy(ctx, "command", text) }
        .padding(12.dp)) {
        Column(Modifier.horizontalScroll(rememberScrollState())) {
            Text(text, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        }
    }
}

@Composable
private fun LinkButton(label: String, url: String, ctx: Context) {
    GhostButton(label, {
        runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }
    }, modifier = Modifier.fillMaxWidth())
}

private fun repoName(repo: String) = repo.trimEnd('/').substringAfterLast('/')

private fun copy(ctx: Context, label: String, value: String) {
    val cb = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    cb.setPrimaryClip(ClipData.newPlainText(label, value))
    Toast.makeText(ctx, "copied", Toast.LENGTH_SHORT).show()
}

/** SHA-256 of the app's signing certificate, colon-separated hex. */
private fun signingCertSha256(ctx: Context): String = try {
    val pm = ctx.packageManager
    val sigs = if (android.os.Build.VERSION.SDK_INT >= 28) {
        pm.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNING_CERTIFICATES)
            .signingInfo?.apkContentsSigners
    } else {
        @Suppress("DEPRECATION")
        pm.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNATURES).signatures
    }
    val cert = sigs?.firstOrNull()?.toByteArray() ?: return "unavailable"
    MessageDigest.getInstance("SHA-256").digest(cert).joinToString(":") { "%02X".format(it) }
} catch (e: Exception) { "unavailable" }
