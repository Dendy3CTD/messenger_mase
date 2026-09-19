package com.mase.messenger.data.session

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.longPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import org.json.JSONObject

private val Context.sessionStore: DataStore<Preferences> by preferencesDataStore(name = "mase_session_v2")

data class SessionSnapshot(
    val accessToken: String,
    val userId: Long,
    val phone: String,
    val userJson: String,
    val darkTheme: Boolean,
    val cachedHost: String,
    val cachedPort: Int
) {
    val isLoggedIn: Boolean get() = accessToken.isNotEmpty() && userId > 0

    fun profile(): PublicUserProfile? = runCatching {
        PublicUserProfile.fromJson(JSONObject(userJson))
    }.getOrNull()

    companion object {
        val Empty = SessionSnapshot(
            accessToken = "",
            userId = 0L,
            phone = "",
            userJson = "{}",
            darkTheme = false,
            cachedHost = "",
            cachedPort = 0
        )
    }
}

data class PublicUserProfile(
    val id: Long,
    val phone: String,
    val username: String,
    val displayName: String,
    val bio: String,
    val avatarMediaId: String = ""
) {
    val needsProfileSetup: Boolean get() = displayName.isBlank()

    fun toJson(): String =
        JSONObject()
            .put("id", id)
            .put("phone", phone)
            .put("username", username)
            .put("displayName", displayName)
            .put("bio", bio)
            .put("avatarMediaId", avatarMediaId)
            .toString()

    companion object {
        fun fromJson(o: JSONObject) = PublicUserProfile(
            id = o.optLong("id"),
            phone = o.optString("phone"),
            username = o.optString("username"),
            displayName = o.optString("displayName"),
            bio = o.optString("bio"),
            avatarMediaId = o.optString("avatarMediaId", "")
        )
    }
}

class SessionRepository(private val context: Context) {

    val snapshot: Flow<SessionSnapshot> = context.sessionStore.data.map { p ->
        SessionSnapshot(
            accessToken = p[KEY_TOKEN] ?: "",
            userId = p[KEY_USER_ID] ?: 0L,
            phone = p[KEY_PHONE] ?: "",
            userJson = p[KEY_USER_JSON] ?: "{}",
            darkTheme = p[KEY_DARK] ?: false,
            cachedHost = p[KEY_CACHED_HOST] ?: "",
            cachedPort = p[KEY_CACHED_PORT] ?: 0
        )
    }

    suspend fun setCachedEndpoint(host: String, port: Int) {
        context.sessionStore.edit {
            it[KEY_CACHED_HOST] = host
            it[KEY_CACHED_PORT] = port
        }
    }

    suspend fun clearCachedEndpoint() {
        context.sessionStore.edit {
            it.remove(KEY_CACHED_HOST)
            it.remove(KEY_CACHED_PORT)
        }
    }

    suspend fun setSession(token: String, userJson: String, phone: String) {
        val id = runCatching { JSONObject(userJson).optLong("id") }.getOrDefault(0L)
        context.sessionStore.edit {
            it[KEY_TOKEN] = token
            it[KEY_USER_JSON] = userJson
            it[KEY_PHONE] = phone
            it[KEY_USER_ID] = id
        }
    }

    suspend fun updateUserJson(userJson: String) {
        val id = runCatching { JSONObject(userJson).optLong("id") }.getOrDefault(0L)
        context.sessionStore.edit {
            it[KEY_USER_JSON] = userJson
            if (id > 0) it[KEY_USER_ID] = id
        }
    }

    suspend fun setDarkTheme(v: Boolean) {
        context.sessionStore.edit { it[KEY_DARK] = v }
    }

    suspend fun logout() {
        context.sessionStore.edit {
            it.remove(KEY_TOKEN)
            it.remove(KEY_USER_ID)
            it.remove(KEY_PHONE)
            it.remove(KEY_USER_JSON)
        }
    }

    companion object {
        private val KEY_TOKEN = stringPreferencesKey("access_token")
        private val KEY_USER_ID = longPreferencesKey("user_id")
        private val KEY_PHONE = stringPreferencesKey("phone_e164")
        private val KEY_USER_JSON = stringPreferencesKey("user_json")
        private val KEY_DARK = booleanPreferencesKey("dark_theme")
        private val KEY_CACHED_HOST = stringPreferencesKey("cached_host")
        private val KEY_CACHED_PORT = intPreferencesKey("cached_port")
    }
}
