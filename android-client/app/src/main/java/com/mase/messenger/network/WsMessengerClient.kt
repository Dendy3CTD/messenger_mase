package com.mase.messenger.network

import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.TimeUnit

private const val TAG = "WsClient"

/**
 * WebSocket messenger client — replaces TcpMessengerClient.
 *
 * Connects to wss://host/ws, sends/receives JSON text frames.
 * Fully compatible with the Go server protocol.
 */
class WsMessengerClient {

    private val client = OkHttpClient.Builder()
        .pingInterval(30, TimeUnit.SECONDS)   // keep-alive pings
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)     // no read timeout (long-lived WS)
        .build()

    @Volatile private var ws: WebSocket? = null

    /**
     * Opens a WebSocket connection to [url].
     *
     * All callbacks fire on OkHttp's internal dispatcher threads.
     *
     * @param url         WebSocket URL, e.g. wss://mase.duckdns.org/ws
     * @param onOpen      called when the connection is established
     * @param onMessage   called for each incoming text frame
     * @param onClosed    called when the connection closes (normal or error)
     */
    fun connect(
        url: String,
        onOpen: () -> Unit,
        onMessage: (String) -> Unit,
        onClosed: (Throwable?) -> Unit,
    ) {
        val request = Request.Builder().url(url).build()
        ws = client.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                Log.d(TAG, "connected to $url")
                onOpen()
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                onMessage(text)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                Log.w(TAG, "failure: ${t.message}")
                ws = null
                onClosed(t)
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                Log.d(TAG, "closed code=$code reason=$reason")
                ws = null
                onClosed(null)
            }
        })
    }

    /**
     * Sends a JSON string as a WebSocket text frame.
     * Safe to call from any thread.
     */
    fun send(json: String) {
        val sent = ws?.send(json) ?: false
        if (!sent) Log.w(TAG, "send failed (not connected?)")
    }

    /** Returns true if the WebSocket is currently open. */
    fun isConnected(): Boolean = ws != null

    /** Closes the WebSocket gracefully. */
    fun close() {
        ws?.close(1000, "client_close")
        ws = null
    }
}
