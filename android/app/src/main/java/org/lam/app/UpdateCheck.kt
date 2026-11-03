package org.lam.app

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/** A release newer than the installed app, with where to get its APK. */
data class UpdateInfo(val tag: String, val apkUrl: String)

private const val RELEASES_LATEST = "https://api.github.com/repos/sacca97/lam/releases/latest"

private fun semver(v: String): List<Int>? {
    val parts = v.removePrefix("v").split('.')
    if (parts.size != 3) return null
    val nums = parts.map { it.toIntOrNull() ?: return null }
    return if (nums.any { it < 0 }) null else nums
}

/** True only when [tag] and [current] are both vX.Y.Z and [tag] is the newer. */
fun isNewer(tag: String, current: String): Boolean {
    val a = semver(tag) ?: return false
    val b = semver(current) ?: return false
    for (i in 0..2) if (a[i] != b[i]) return a[i] > b[i]
    return false
}

/** The latest release with an APK, or null on any failure. */
fun fetchLatestRelease(): UpdateInfo? = runCatching {
    val conn = URL(RELEASES_LATEST).openConnection() as HttpURLConnection
    conn.connectTimeout = 10_000
    conn.readTimeout = 10_000
    conn.setRequestProperty("User-Agent", "lam-android")
    conn.setRequestProperty("Accept", "application/vnd.github+json")
    try {
        if (conn.responseCode != 200) return@runCatching null
        val json = JSONObject(conn.inputStream.bufferedReader().use { it.readText() })
        val assets = json.getJSONArray("assets")
        for (i in 0 until assets.length()) {
            val asset = assets.getJSONObject(i)
            if (asset.getString("name") == "lam-android.apk") {
                return@runCatching UpdateInfo(json.getString("tag_name"), asset.getString("browser_download_url"))
            }
        }
        null
    } finally {
        conn.disconnect()
    }
}.getOrNull()
