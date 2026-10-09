package com.localghost.app.net

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.KeyProtection
import android.util.Base64
import com.localghost.app.security.BoxConfig
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.io.ByteArrayInputStream
import java.net.Socket
import java.security.KeyFactory
import java.security.Principal
import java.security.PrivateKey
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.security.spec.PKCS8EncodedKeySpec
import javax.net.ssl.X509KeyManager
import kotlinx.coroutines.sync.withLock

/**
 * The device identity: a client certificate and the private key the phone presents in the mutual
 * TLS handshake with the box.
 *
 * WHERE THE KEY LIVES. The enrolment QR carries a certificate AND its private key (the box made
 * both, so enrolment is one scan). Since 30 Sep 2026 that key goes straight into the phone's
 * secure element (AndroidKeyStore, imported as non-exportable): no copy of it remains in the app's
 * files, and code running as the app can use it but cannot read it out. A phone enrolled before
 * moves its key there the first time it connects.
 *
 * THEN IT IS REPLACED. The QR's key existed off the phone (on the box while the QR was drawn, in
 * any photo of the QR). After the first PIN unlock the phone makes a key of its own INSIDE the
 * Keystore, proves it holds it, and the box signs a certificate for it (secd, rekey.go). The phone
 * switches over and confirms over the new certificate; the box then retires the QR's: anything that
 * presents it is answered as if the box were down, the PIN entry included. Until the confirmation
 * both work, so a lost answer costs nothing: the next unlock finishes the switch.
 *
 * AND AGAIN, EVERY DAY. Since 9 October 2026 a certificate is good for two weeks, and the same
 * switch runs again at every unlock once the certificate presented is a day old: a new key, a new
 * certificate, the old one retired. A phone in use never sees the end of its certificate, like a
 * cookie extended while it is used; a phone left for two weeks presents one the box's front door
 * refuses, and the person scans a fresh QR. [expired] says so before the unlock tries.
 */
object DeviceCert {

    private const val ALIAS = "device-cert" // the certificate's name in BoxConfig's store (and the QR key's, before the Keystore)
    private const val PREFS = "lg_device_key"
    private const val KS_PREFIX = "localghost.device."
    private const val REKEY_MESSAGE_V2 = "localghost rekey v2\n"
    /** A certificate is renewed once this old (the box backdates each by an hour; renewal a day on). */
    private const val RENEW_AFTER_MS = 24L * 3600_000L

    /** Parsed device identity. */
    data class Identity(val certificate: X509Certificate, val privateKey: PrivateKey)

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    private fun ks(): KeyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    /** The Keystore alias presented now; "" while the key is still the app's wrapped copy. */
    fun activeAlias(ctx: Context): String = prefs(ctx).getString("alias", "") ?: ""

    /** True once the phone presents a key it made itself (the QR's is retired on the box). */
    fun rotated(ctx: Context): Boolean = prefs(ctx).getBoolean("rotated", false)

    /** The certificate presented now: when it was made and when it runs out; null when not enrolled. */
    data class CertDates(val notBefore: Long, val notAfter: Long)
    fun dates(ctx: Context): CertDates? {
        val pem = BoxConfig.readSecret(ctx, "$ALIAS.cert") ?: return null
        val c = try { parseCert(pem) } catch (e: Exception) { return null }
        return CertDates(c.notBefore.time, c.notAfter.time)
    }

    /** The certificate has run out (two weeks without an unlock): the box's door refuses it, and a
     *  fresh QR is the way back in. */
    fun expired(ctx: Context): Boolean = dates(ctx)?.let { it.notAfter < System.currentTimeMillis() } ?: false

    /** A day old or more (the box backdates a certificate by an hour, so a day and an hour since
     *  it was made): time for the next one. */
    fun renewDue(ctx: Context): Boolean = dates(ctx)?.let { System.currentTimeMillis() - it.notBefore >= RENEW_AFTER_MS + 3600_000L } ?: false

    /** A new enrolment (a scanned QR): whatever this phone presented before goes; the QR's key goes
     *  into the Keystore at once. */
    fun store(ctx: Context, certPem: String, keyPkcs8Pem: String) {
        dropKeystoreKeys(except = "")
        prefs(ctx).edit().clear().commit()
        BoxConfig.writeSecret(ctx, "$ALIAS.cert", certPem, now = true)
        BoxConfig.removeSecret(ctx, "$ALIAS.cert.next")
        if (!importKey(ctx, certPem, keyPkcs8Pem, KS_PREFIX + "1")) {
            // the Keystore would not take it: kept wrapped in the app's store, as before this build
            BoxConfig.writeSecret(ctx, "$ALIAS.key", keyPkcs8Pem, now = true)
        }
    }

    /** Un-enrolment: the keys leave the Keystore too. */
    fun forget(ctx: Context) {
        dropKeystoreKeys(except = "")
        prefs(ctx).edit().clear().commit()
    }

    fun isEnrolled(ctx: Context): Boolean =
        BoxConfig.readSecret(ctx, "$ALIAS.cert") != null &&
            (activeAlias(ctx).isNotEmpty() || BoxConfig.readSecret(ctx, "$ALIAS.key") != null)

    fun load(ctx: Context): Identity? {
        val certPem = BoxConfig.readSecret(ctx, "$ALIAS.cert") ?: return null
        val cert = try { parseCert(certPem) } catch (e: Exception) { return null }
        val alias = activeAlias(ctx)
        if (alias.isNotEmpty()) {
            val key = try { ks().getKey(alias, null) as? PrivateKey } catch (e: Exception) { null }
            if (key != null) return Identity(cert, key)
        }
        // enrolled before 30 Sep 2026: the QR's key, wrapped in the app's store. Into the Keystore now.
        val keyPem = BoxConfig.readSecret(ctx, "$ALIAS.key") ?: return null
        val soft = try { parseKey(keyPem) } catch (e: Exception) { return null }
        if (importKey(ctx, certPem, keyPem, KS_PREFIX + "1")) {
            val key = try { ks().getKey(KS_PREFIX + "1", null) as? PrivateKey } catch (e: Exception) { null }
            if (key != null) return Identity(cert, key)
        }
        return Identity(cert, soft)
    }

    /** The QR's key into the Keystore, non-exportable; the app's wrapped copy removed. */
    private fun importKey(ctx: Context, certPem: String, keyPem: String, alias: String): Boolean = try {
        val cert = parseCert(certPem)
        val key = parseKey(keyPem)
        val prot = KeyProtection.Builder(KeyProperties.PURPOSE_SIGN)
            .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384, KeyProperties.DIGEST_SHA512)
        if (key.algorithm == "RSA") prot.setSignaturePaddings(KeyProperties.SIGNATURE_PADDING_RSA_PKCS1, KeyProperties.SIGNATURE_PADDING_RSA_PSS)
        ks().setEntry(alias, KeyStore.PrivateKeyEntry(key, arrayOf<java.security.cert.Certificate>(cert)), prot.build())
        prefs(ctx).edit().putString("alias", alias).commit()
        BoxConfig.removeSecret(ctx, "$ALIAS.key")
        BoxHttp.reset()
        true
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "device key not moved into the Keystore: ${e.javaClass.simpleName} ${e.message}")
        false
    }

    /**
     * After a PIN unlock: the phone's own key replaces the QR's the first time, and a new key
     * replaces the one a day old every time after (see the top of this file). True when the phone
     * presents a key the box has signed within the day and the one before is retired. Blocking
     * network and Keystore work: call it off the main thread.
     */
    suspend fun rotateIfNeeded(ctx: Context): Boolean = turn.withLock { rotateLocked(ctx) }

    // one switch at a time: the unlock and the background poll can both ask on the same minute
    private val turn = kotlinx.coroutines.sync.Mutex()

    private suspend fun rotateLocked(ctx: Context): Boolean {
        val p = prefs(ctx)
        val rotated = p.getBoolean("rotated", false)
        if (rotated && !p.getBoolean("confirm_pending", false) && p.getString("next_alias", "").isNullOrEmpty() && !renewDue(ctx)) return true
        finishSwitch(ctx) // a switch the app died in the middle of
        if (p.getBoolean("confirm_pending", false)) return confirm(ctx)
        if (activeAlias(ctx).isEmpty() && load(ctx) != null && activeAlias(ctx).isEmpty()) return false // not in the Keystore: not rotated from here
        val cur = activeAlias(ctx)
        if (cur.isEmpty()) return false
        val presented = BoxConfig.readSecret(ctx, "$ALIAS.cert")?.let { try { parseCert(it) } catch (_: Exception) { null } } ?: return false
        // THE FIRST TIME a key of the phone's own replaces the QR's; EVERY DAY AFTER the same key
        // gets a new certificate: the key never left the secure element, and keeping it keeps the
        // phone's name on the box (the box names a phone by its public key)
        val fresh = !rotated
        val alias = if (fresh) KS_PREFIX + ((cur.removePrefix(KS_PREFIX).toIntOrNull() ?: 1) + 1) else cur
        return try {
            val (pub, priv) = if (fresh) {
                try { ks().deleteEntry(alias) } catch (_: Exception) { }
                val kpg = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore")
                kpg.initialize(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384, KeyProperties.DIGEST_SHA512)
                    .build())
                val kp = kpg.generateKeyPair()
                kp.public.encoded to kp.private
            } else {
                val key = ks().getKey(cur, null) as? PrivateKey ?: return false
                presented.publicKey.encoded to key
            }
            val spki = pub
            // the proof, bound to the certificate presented: a proof taken from one phone is no
            // proof from another (secd rekey.go, v2)
            val bound = REKEY_MESSAGE_V2.toByteArray() + derHex(presented.encoded).toByteArray() + "\n".toByteArray() + spki
            val sig = Signature.getInstance("SHA256withECDSA").run { initSign(priv); update(bound); sign() }
            val certPem = BoxClient.deviceRekey(ctx, spki, sig)
            if (certPem == null || !parseCert(certPem).publicKey.encoded.contentEquals(spki)) {
                if (fresh) try { ks().deleteEntry(alias) } catch (_: Exception) { }
                return false // the next unlock or poll tries again
            }
            // two steps, each committed: the certificate kept beside the key, then the switch
            BoxConfig.writeSecret(ctx, "$ALIAS.cert.next", certPem, now = true)
            p.edit().putString("next_alias", alias).commit()
            finishSwitch(ctx)
            confirm(ctx)
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "device key rotation: ${e.javaClass.simpleName} ${e.message}")
            false
        }
    }

    /** SHA-256 of a certificate's DER, hex: how secd names the certificate in the bound proof. */
    private fun derHex(der: ByteArray): String =
        java.security.MessageDigest.getInstance("SHA-256").digest(der).joinToString("") { "%02x".format(it) }

    /** The new key and its certificate become the ones presented. */
    private fun finishSwitch(ctx: Context) {
        val p = prefs(ctx)
        val next = p.getString("next_alias", "") ?: ""
        if (next.isEmpty()) return
        val certPem = BoxConfig.readSecret(ctx, "$ALIAS.cert.next")
        val hasKey = try { ks().containsAlias(next) } catch (_: Exception) { false }
        if (certPem == null || !hasKey) {
            p.edit().remove("next_alias").commit()
            BoxConfig.removeSecret(ctx, "$ALIAS.cert.next")
            return
        }
        BoxConfig.writeSecret(ctx, "$ALIAS.cert", certPem, now = true)
        p.edit().putString("alias", next).remove("next_alias").putBoolean("confirm_pending", true).commit()
        BoxConfig.removeSecret(ctx, "$ALIAS.cert.next")
        BoxHttp.reset() // the next connection presents the new key
    }

    /** Over the new certificate: the box retires the QR's. Then the old key leaves the Keystore. */
    private suspend fun confirm(ctx: Context): Boolean {
        if (!BoxClient.deviceRekeyConfirm(ctx)) return false // both certificates work until the next unlock
        dropKeystoreKeys(except = activeAlias(ctx))
        prefs(ctx).edit().putBoolean("rotated", true).remove("confirm_pending").putLong("renewed_at", System.currentTimeMillis()).commit()
        android.util.Log.i("LocalGhost", "device key rotated: this phone presents a key it made today, and the one before is retired")
        return true
    }

    private fun dropKeystoreKeys(except: String) {
        try {
            val ks = ks()
            for (a in ks.aliases().toList()) if (a.startsWith(KS_PREFIX) && a != except) ks.deleteEntry(a)
        } catch (_: Exception) { }
    }

    /** Wrap the stored identity as an X509KeyManager for BoxTrust.socketFactory. */
    fun keyManager(ctx: Context): X509KeyManager? {
        val id = load(ctx) ?: return null
        return SingleIdentityKeyManager(id)
    }

    private fun parseCert(pem: String): X509Certificate {
        val der = pemBody(pem, "CERTIFICATE")
        val cf = CertificateFactory.getInstance("X.509")
        return cf.generateCertificate(ByteArrayInputStream(der)) as X509Certificate
    }

    private fun parseKey(pem: String): PrivateKey {
        val der = pemBody(pem, "PRIVATE KEY")
        // EC keys (the box uses P-256); fall back to RSA if ever needed.
        return try {
            KeyFactory.getInstance("EC").generatePrivate(PKCS8EncodedKeySpec(der))
        } catch (e: Exception) {
            KeyFactory.getInstance("RSA").generatePrivate(PKCS8EncodedKeySpec(der))
        }
    }

    private fun pemBody(pem: String, label: String): ByteArray {
        val base64 = pem
            .replace("-----BEGIN $label-----", "")
            .replace("-----END $label-----", "")
            .replace("\\s".toRegex(), "")
        return Base64.decode(base64, Base64.DEFAULT)
    }

    /** A KeyManager that always presents the one device identity (we have exactly one). */
    private class SingleIdentityKeyManager(private val id: Identity) : X509KeyManager {
        private val alias = "device"
        override fun chooseClientAlias(keyType: Array<out String>?, issuers: Array<out Principal>?, socket: Socket?) = alias
        override fun getCertificateChain(alias: String?) = arrayOf(id.certificate)
        override fun getPrivateKey(alias: String?) = id.privateKey
        override fun getClientAliases(keyType: String?, issuers: Array<out Principal>?) = arrayOf(alias)
        // Server-side methods unused on the phone.
        override fun chooseServerAlias(keyType: String?, issuers: Array<out Principal>?, socket: Socket?): String? = null
        override fun getServerAliases(keyType: String?, issuers: Array<out Principal>?): Array<String>? = null
    }
}
