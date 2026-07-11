package com.localghost.app.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Encrypted store for the box connection details written at enrollment: base URL, device token,
 * and device name. Persisted as AES-GCM ciphertext (key in AndroidKeyStore, same approach as
 * AppLock) inside private SharedPreferences. The public release APK ships with these EMPTY; the
 * app is "configured" once enrollment writes them. That empty-vs-present check is the
 * setup-vs-use signal.
 *
 * This is NOT the auth credential (the mTLS client cert is, via DeviceCert , the box-issued cert +
 * key delivered in the enrolment QR). The token here is an enrollment/identifier value the box hands
 * back; losing it just means re-enrolling.
 */
object BoxConfig {

    private const val PREFS = "lg_box_config"
    private const val ALIAS = "localghost.boxconfig"
    private const val TRANSFORM = "AES/GCM/NoPadding"
    private const val GCM_TAG_BITS = 128

    private const val K_URL = "url"
    private const val K_TOKEN = "token"
    private const val K_NAME = "device_name"
    private const val K_FP = "cert_fp"

    data class Config(
        val baseUrl: String,
        val deviceToken: String,
        val deviceName: String,
        val certFingerprint: String = "",
    )

    /** True once enrollment has stored a usable box URL + token. Drives setup vs use. */
    fun isConfigured(ctx: Context): Boolean {
        val p = prefs(ctx)
        // If the encrypted values are PRESENT on disk, the device was enrolled , full stop. Whether we
        // can decrypt them THIS launch is a separate question: a transient keystore hiccup must NOT be
        // read as "never enrolled" and bounce the user to re-scan (the reported bug). So configured =
        // the ciphertext exists. read() still returns null if decryption fails, and callers that need
        // the actual values handle that, but the ENROLLMENT decision keys off presence, not decryptability.
        val hasUrl = !p.getString(K_URL, null).isNullOrBlank()
        val hasToken = !p.getString(K_TOKEN, null).isNullOrBlank()
        return hasUrl && hasToken
    }

    fun read(ctx: Context): Config? {
        val p = prefs(ctx)
        val url = decrypt(p.getString(K_URL, null)) ?: return null
        val token = decrypt(p.getString(K_TOKEN, null)) ?: return null
        val name = decrypt(p.getString(K_NAME, null)) ?: ""
        val fp = decrypt(p.getString(K_FP, null)) ?: ""
        return Config(url, token, name, fp)
    }

    fun write(ctx: Context, config: Config) {
        prefs(ctx).edit()
            .putString(K_URL, encrypt(config.baseUrl))
            .putString(K_TOKEN, encrypt(config.deviceToken))
            .putString(K_NAME, encrypt(config.deviceName))
            .putString(K_FP, encrypt(config.certFingerprint))
            .apply()
    }

    /** Wipe the local connection details (e.g. on un-enroll or a global wipe). */
    fun clear(ctx: Context) {
        prefs(ctx).edit().clear().apply()
    }

    /** Store an arbitrary named secret, encrypted with the same keystore-backed AES-GCM as the
     *  config fields. Used by DeviceCert for the device cert + private key delivered via the QR. */
    fun writeSecret(ctx: Context, name: String, value: String) {
        prefs(ctx).edit().putString("secret.$name", encrypt(value)).apply()
    }

    fun readSecret(ctx: Context, name: String): String? =
        decrypt(prefs(ctx).getString("secret.$name", null))

    // --- crypto: AES-GCM with a keystore key, ciphertext = base64(iv || ct) ---

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    // Synchronized so concurrent callers (the launch isConfigured() check racing the background sync
    // and poll workers, all of which touch the box on startup) cannot both fall into the "generate"
    // branch. A regenerated key makes ALL existing ciphertext undecryptable, which read()/isConfigured()
    // would then misread as "never enrolled" and silently force a re-scan , the exact bug being fixed.
    @Synchronized
    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val spec = KeyGenParameterSpec.Builder(
            ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            // Not bound to user auth: setup is written before the gate, and read on every launch
            // to decide setup vs use. The values are not the auth secret.
            .build()
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            .apply { init(spec) }.generateKey()
    }

    private fun encrypt(plain: String): String {
        val cipher = Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key()) }
        val iv = cipher.iv
        val ct = cipher.doFinal(plain.toByteArray(Charsets.UTF_8))
        return Base64.encodeToString(iv + ct, Base64.NO_WRAP)
    }

    private fun decrypt(stored: String?): String? {
        if (stored.isNullOrBlank()) return null
        return try {
            val blob = Base64.decode(stored, Base64.NO_WRAP)
            val iv = blob.copyOfRange(0, 12)             // GCM IV is 12 bytes
            val ct = blob.copyOfRange(12, blob.size)
            val cipher = Cipher.getInstance(TRANSFORM).apply {
                init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(GCM_TAG_BITS, iv))
            }
            String(cipher.doFinal(ct), Charsets.UTF_8)
        } catch (e: Exception) {
            null   // corrupt/cleared/unreadable -> treat as unconfigured
        }
    }
}
