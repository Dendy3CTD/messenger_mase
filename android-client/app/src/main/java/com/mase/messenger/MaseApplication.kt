package com.mase.messenger

import android.app.Application
import com.mase.messenger.messaging.MessengerEngine

class MaseApplication : Application() {
    lateinit var engine: MessengerEngine
        private set

    override fun onCreate() {
        super.onCreate()
        engine = MessengerEngine(this)
        engine.start()
    }
}
