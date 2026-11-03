package org.lam.app

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
 * SSH passwords the person chose to remember. Each is encrypted with an AES-GCM key
 * that lives in the Android Keystore (hardware-backed where the phone has it and never
 * readable by the app or by a backup); only the ciphertext is kept in the app's
 * private preferences. Nothing leaves the phone.
 */
class Vault(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("lam-vault", Context.MODE_PRIVATE)

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    fun put(account: String, password: String) {
        runCatching {
            val c = Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key()) }
            val sealed = c.doFinal(password.toByteArray())
            val blob = Base64.encodeToString(c.iv, Base64.NO_WRAP) + ":" + Base64.encodeToString(sealed, Base64.NO_WRAP)
            prefs.edit().putString(account, blob).apply()
        }
    }

    fun get(account: String): String? {
        val blob = prefs.getString(account, null) ?: return null
        return runCatching {
            val (iv, sealed) = blob.split(":", limit = 2).map { Base64.decode(it, Base64.NO_WRAP) }
            val c = Cipher.getInstance(TRANSFORM).apply { init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, iv)) }
            String(c.doFinal(sealed))
        }.getOrElse { forget(account); null } // a key that changed or data that is damaged is as good as none
    }

    fun forget(account: String) = prefs.edit().remove(account).apply()

    companion object {
        private const val ALIAS = "lam-ssh-passwords"
        private const val TRANSFORM = "AES/GCM/NoPadding"
        fun account(user: String, host: String) = "$user@$host"
    }
}
