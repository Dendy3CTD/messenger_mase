package com.mase.messenger.discovery

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import kotlin.coroutines.resume

private const val TAG = "ServerDiscovery"
private const val BEACON_PORT = 5553  // tcpPort - 2

class ServerDiscovery(private val context: Context) {

    /**
     * Discovers the Mase server on the LAN.
     * Strategy: UDP broadcast listener (fast, ~1-3s) + mDNS in parallel.
     * First one to succeed wins.
     */
    suspend fun discover(timeoutMs: Long = 12_000): Pair<String, Int>? =
        withContext(Dispatchers.IO) {
            suspendCancellableCoroutine { cont ->
                var resolved = false

                fun finish(value: Pair<String, Int>?) {
                    synchronized(this) {
                        if (resolved) return
                        resolved = true
                    }
                    cont.resume(value)
                }

                // ── 1. UDP beacon listener ────────────────────────────────────
                val udpThread = Thread {
                    try {
                        val sock = DatagramSocket(null)
                        sock.reuseAddress = true
                        sock.soTimeout = timeoutMs.toInt()
                        sock.bind(InetSocketAddress(BEACON_PORT))
                        val buf = ByteArray(256)
                        val pkt = DatagramPacket(buf, buf.size)
                        sock.receive(pkt)
                        val json = String(pkt.data, 0, pkt.length)
                        val obj = JSONObject(json)
                        if (obj.optInt("mase") == 1) {
                            val port = obj.optInt("port", 5555)
                            val ip = pkt.address.hostAddress ?: return@Thread
                            Log.d(TAG, "UDP beacon from $ip:$port")
                            sock.close()
                            finish(ip to port)
                        }
                        sock.close()
                    } catch (e: Exception) {
                        Log.d(TAG, "UDP beacon timeout/error: ${e.message}")
                    }
                }
                udpThread.isDaemon = true
                udpThread.start()

                // ── 2. mDNS fallback ──────────────────────────────────────────
                val nsd = context.getSystemService(Context.NSD_SERVICE) as NsdManager
                val mainHandler = Handler(Looper.getMainLooper())
                val holder = arrayOfNulls<NsdManager.DiscoveryListener>(1)

                holder[0] = object : NsdManager.DiscoveryListener {
                    override fun onStartDiscoveryFailed(st: String?, err: Int) {}
                    override fun onStopDiscoveryFailed(st: String?, err: Int) {}
                    override fun onDiscoveryStarted(st: String?) {}
                    override fun onDiscoveryStopped(st: String?) {}
                    override fun onServiceLost(info: NsdServiceInfo?) {}

                    override fun onServiceFound(serviceInfo: NsdServiceInfo?) {
                        if (serviceInfo == null) return
                        val st = serviceInfo.serviceType ?: return
                        if (!st.contains("mase", ignoreCase = true)) return
                        try {
                            nsd.resolveService(serviceInfo, object : NsdManager.ResolveListener {
                                override fun onResolveFailed(i: NsdServiceInfo?, err: Int) {}
                                override fun onServiceResolved(info: NsdServiceInfo) {
                                    if (resolved) return
                                    val host: String? = if (Build.VERSION.SDK_INT >= 34) {
                                        @Suppress("NewApi")
                                        info.hostAddresses.firstOrNull()?.hostAddress
                                    } else null
                                        ?: run {
                                            @Suppress("DEPRECATION")
                                            info.host?.hostAddress
                                        }
                                    if (host == null || host.startsWith("127.")) return
                                    val port = info.port
                                    if (port > 0) {
                                        Log.d(TAG, "mDNS resolved $host:$port")
                                        finish(host to port)
                                        try { holder[0]?.let { nsd.stopServiceDiscovery(it) } }
                                        catch (_: Exception) {}
                                    }
                                }
                            })
                        } catch (_: Exception) {}
                    }
                }

                try {
                    nsd.discoverServices("_mase._tcp", NsdManager.PROTOCOL_DNS_SD, holder[0]!!)
                } catch (_: Exception) {}

                // ── Timeout ───────────────────────────────────────────────────
                mainHandler.postDelayed({
                    udpThread.interrupt()
                    try { holder[0]?.let { nsd.stopServiceDiscovery(it) } } catch (_: Exception) {}
                    finish(null)
                }, timeoutMs)

                cont.invokeOnCancellation {
                    udpThread.interrupt()
                    try { holder[0]?.let { nsd.stopServiceDiscovery(it) } } catch (_: Exception) {}
                }
            }
        }
}
