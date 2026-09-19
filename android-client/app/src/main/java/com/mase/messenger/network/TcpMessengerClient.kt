package com.mase.messenger.network

import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.Socket

class TcpMessengerClient {

    @Volatile
    private var socket: Socket? = null
    @Volatile
    private var writer: BufferedWriter? = null

    /**
     * @param firstOutgoingLine optional first line (e.g. null for OTP handshake before auth).
     */
    fun connect(
        host: String,
        port: Int,
        firstOutgoingLine: String?,
        onConnected: () -> Unit,
        onLine: (String) -> Unit,
        onError: (Throwable) -> Unit
    ) {
        try {
            val newSocket = Socket(host, port)
            val newWriter = BufferedWriter(OutputStreamWriter(newSocket.getOutputStream(), Charsets.UTF_8))
            val reader = BufferedReader(InputStreamReader(newSocket.getInputStream(), Charsets.UTF_8))

            socket = newSocket
            writer = newWriter

            if (firstOutgoingLine != null) {
                newWriter.write(firstOutgoingLine)
                newWriter.newLine()
                newWriter.flush()
            }

            onConnected()

            while (true) {
                val line = reader.readLine() ?: break
                onLine(line)
            }
        } catch (t: Throwable) {
            onError(t)
        } finally {
            close()
        }
    }

    fun sendJsonLine(json: String) {
        val w = writer ?: return
        synchronized(this) {
            w.write(json)
            w.newLine()
            w.flush()
        }
    }

    fun isConnected(): Boolean = socket != null && socket!!.isConnected && !socket!!.isClosed

    fun close() {
        runCatching { writer?.close() }
        runCatching { socket?.close() }
        writer = null
        socket = null
    }
}
