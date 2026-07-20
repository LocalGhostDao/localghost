package com.localghost.app.security

import android.app.KeyguardManager
import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Log
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

/**
 * Biometric gate key. A keystore AES key bound to user authentication. The key carries a 10-SECOND
 * validity window (setUserAuthenticationParameters(10, ...)): any device authentication , INCLUDING
 * the phone's own lockscreen unlock , opens the window, and within it the cipher inits silently.
 * That is the whole "just unlocked my phone onto the app" feature: the OS itself vouches for the
 * recency, so the app skips its own fingerprint prompt and goes straight to the box PIN. Past the
 * window, init throws UserNotAuthenticatedException and the app shows the biometric prompt (WITHOUT
 * a CryptoObject , duration-bound keys use the time-window pattern, not per-use crypto binding),
 * then retries.
 *
 * Alias is v2: the v1 key was per-use (duration 0), which is a different keystore contract; an
 * existing install's v1 key is deleted and replaced. Safe , this key only gates the phone UI, the
 * box PIN is the real credential, and the box never trusts this key.
 */
object AppLock {
    private const val ALIAS = "localghost.gate.v2"
    private const val OLD_ALIAS = "localghost.gate"
    private const val TRANSFORM = "AES/GCM/NoPadding"
    private const val AUTH_WINDOW_SECONDS = 10

    fun deviceAuthAvailable(ctx: Context): Boolean =
        ctx.getSystemService(KeyguardManager::class.java)?.isDeviceSecure == true

    fun ensureKey(ctx: Context): Boolean {
        if (!deviceAuthAvailable(ctx)) return false
        return ensureKey()
    }

    private fun ensureKey(): Boolean {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        try { if (ks.containsAlias(OLD_ALIAS)) ks.deleteEntry(OLD_ALIAS) } catch (_: Exception) { /* gone is fine */ }
        if (ks.containsAlias(ALIAS)) return true
        return generate()
    }

    private fun generate(): Boolean = try {
        val spec = KeyGenParameterSpec.Builder(
            ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setUserAuthenticationRequired(true)
            .setUserAuthenticationParameters(
                AUTH_WINDOW_SECONDS, KeyProperties.AUTH_BIOMETRIC_STRONG or KeyProperties.AUTH_DEVICE_CREDENTIAL)
            .build()
        KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            .apply { init(spec) }.generateKey()
        true
    } catch (e: Exception) {
        Log.w("LocalGhost", "phone gate key unavailable: ${e.message}")
        false
    }

    /**
     * The SILENT gate: a Cipher when the device was authenticated within the last 10 seconds (the
     * lockscreen unlock that put the app on screen counts), null when a prompt is needed. Catch
     * order matters: UserNotAuthenticatedException EXTENDS InvalidKeyException, so the window-miss
     * case must be caught first or it would be misread as an invalidated key and trigger a
     * pointless regenerate.
     *
     * A genuinely invalidated key (biometric enrolment changed) is regenerated here, bound to the
     * CURRENT biometric set; the fresh key has no open window yet, so the retry lands in the
     * prompt path , correct, since the biometric set just changed.
     */
    fun tryGateCipher(): Cipher? = try {
        initCipher()
    } catch (e: android.security.keystore.UserNotAuthenticatedException) {
        null // outside the window: prompt needed
    } catch (e: java.security.InvalidKeyException) {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        try { ks.deleteEntry(ALIAS) } catch (_: Exception) { /* already gone is fine */ }
        if (!generate()) return null
        try {
            initCipher()
        } catch (e2: android.security.keystore.UserNotAuthenticatedException) {
            null
        }
    }

    @Deprecated("per-use CryptoObject flow, replaced by tryGateCipher + windowed key", ReplaceWith("tryGateCipher()"))
    fun gateCipher(): Cipher = tryGateCipher()
        ?: throw java.security.InvalidKeyException("gate window closed; authenticate first")

    private fun initCipher(): Cipher {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        val key = ks.getKey(ALIAS, null) as? SecretKey
            ?: throw java.security.InvalidKeyException("gate key missing")
        return Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key) }
    }
}
