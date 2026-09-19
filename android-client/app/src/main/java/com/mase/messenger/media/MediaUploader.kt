package com.mase.messenger.media

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL

object MediaUploader {

    private const val TAG = "MediaUploader"
    private const val MAX_SIDE = 1280
    private const val JPEG_QUALITY = 85

    /**
     * Uploads a compressed JPEG to the Mase media server.
     *
     * @param baseUrl  e.g. "https://mase.duckdns.org" (no trailing slash)
     * @param token    Bearer token
     * @param uri      photo URI from gallery picker
     * @return         mediaId filename on success, null on failure
     */
    suspend fun uploadPhoto(
        context: Context,
        baseUrl: String,
        token: String,
        uri: Uri,
    ): String? = withContext(Dispatchers.IO) {
        try {
            val jpeg = compressImage(context, uri) ?: return@withContext null
            upload(baseUrl, token, jpeg, "image/jpeg")
        } catch (e: Exception) {
            Log.e(TAG, "Upload failed", e)
            null
        }
    }

    /**
     * Uploads raw bytes with the given content type.
     * Used for voice messages, video, files.
     */
    suspend fun uploadRaw(
        baseUrl: String,
        token: String,
        data: ByteArray,
        contentType: String,
    ): String? = withContext(Dispatchers.IO) {
        try {
            upload(baseUrl, token, data, contentType)
        } catch (e: Exception) {
            Log.e(TAG, "Upload failed", e)
            null
        }
    }

    private fun upload(baseUrl: String, token: String, data: ByteArray, contentType: String): String? {
        val url = URL("$baseUrl/upload")
        val conn = url.openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.setRequestProperty("Authorization", "Bearer $token")
        conn.setRequestProperty("Content-Type", contentType)
        conn.setRequestProperty("Content-Length", data.size.toString())
        conn.doOutput = true
        conn.connectTimeout = 15_000
        conn.readTimeout = 90_000

        conn.outputStream.use { it.write(data) }

        if (conn.responseCode != 200) {
            Log.w(TAG, "Upload HTTP ${conn.responseCode}")
            return null
        }

        val response = conn.inputStream.bufferedReader().readText()
        return JSONObject(response).optString("mediaId").takeIf { it.isNotEmpty() }
    }

    /** Returns the full URL for a media file. */
    fun mediaUrl(baseUrl: String, mediaId: String): String =
        "$baseUrl/media/$mediaId"

    private fun compressImage(context: Context, uri: Uri): ByteArray? {
        return try {
            val input: InputStream = context.contentResolver.openInputStream(uri) ?: return null
            val original = BitmapFactory.decodeStream(input)
            input.close()
            if (original == null) return null

            val (w, h) = scaledSize(original.width, original.height)
            val scaled = if (w == original.width && h == original.height) original
            else Bitmap.createScaledBitmap(original, w, h, true)

            val out = ByteArrayOutputStream()
            scaled.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, out)
            out.toByteArray()
        } catch (e: Exception) {
            Log.e(TAG, "Compress failed", e)
            null
        }
    }

    private fun scaledSize(w: Int, h: Int): Pair<Int, Int> {
        if (w <= MAX_SIDE && h <= MAX_SIDE) return w to h
        return if (w >= h) MAX_SIDE to (h * MAX_SIDE / w)
        else (w * MAX_SIDE / h) to MAX_SIDE
    }
}
