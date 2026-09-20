package com.mase.messenger.data.local

import android.content.Context
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Insert
import androidx.room.migration.Migration
import androidx.room.OnConflictStrategy
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.Room
import androidx.room.RoomDatabase
import androidx.sqlite.db.SupportSQLiteDatabase
import kotlinx.coroutines.flow.Flow

@Entity(tableName = "chats")
data class ChatEntity(
    @PrimaryKey val chatId: Long,
    val kind: String,           // "global" | "direct" | "group"
    val title: String,
    val peerUserId: Long?,
    val peerDisplayName: String = "",
    val lastSnippet: String,
    val lastTs: Long,
    val unreadCount: Int = 0
)

@Entity(tableName = "messages")
data class MessageEntity(
    @PrimaryKey val id: Long,
    val chatId: Long,
    val senderId: Long,
    val body: String,
    val ts: Long,
    val status: String,
    val msgType: String = "text",   // "text" | "photo"
    val mediaId: String = "",
    val senderName: String = ""     // for group chats — display name of sender
)

@Dao
interface ChatDao {
    @Query("SELECT * FROM chats ORDER BY lastTs DESC")
    fun observeChats(): Flow<List<ChatEntity>>

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun upsert(chat: ChatEntity)

    @Query("SELECT * FROM chats WHERE chatId = :chatId LIMIT 1")
    suspend fun getById(chatId: Long): ChatEntity?

    @Query("UPDATE chats SET unreadCount = 0 WHERE chatId = :chatId")
    suspend fun resetUnread(chatId: Long)

    @Query("UPDATE chats SET unreadCount = unreadCount + 1 WHERE chatId = :chatId")
    suspend fun incrementUnread(chatId: Long)

    @Query("DELETE FROM chats WHERE chatId = :chatId")
    suspend fun deleteById(chatId: Long)
}

@Dao
interface MessageDao {
    @Query("SELECT * FROM messages WHERE chatId = :chatId ORDER BY ts ASC")
    fun observe(chatId: Long): Flow<List<MessageEntity>>

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun insert(m: MessageEntity)

    @Query("UPDATE messages SET status = :status WHERE id = :id")
    suspend fun updateStatus(id: Long, status: String)
}

@Database(entities = [ChatEntity::class, MessageEntity::class], version = 3, exportSchema = true)
abstract class MaseDatabase : RoomDatabase() {
    abstract fun chatDao(): ChatDao
    abstract fun messageDao(): MessageDao

    companion object {
        private val MIGRATION_1_2 = object : Migration(1, 2) {
            override fun migrate(database: SupportSQLiteDatabase) {
                database.execSQL("ALTER TABLE messages ADD COLUMN msgType TEXT NOT NULL DEFAULT 'text'")
                database.execSQL("ALTER TABLE messages ADD COLUMN mediaId TEXT NOT NULL DEFAULT ''")
                database.execSQL("ALTER TABLE chats ADD COLUMN peerDisplayName TEXT NOT NULL DEFAULT ''")
                database.execSQL("ALTER TABLE chats ADD COLUMN unreadCount INTEGER NOT NULL DEFAULT 0")
            }
        }

        private val MIGRATION_2_3 = object : Migration(2, 3) {
            override fun migrate(database: SupportSQLiteDatabase) {
                database.execSQL("ALTER TABLE messages ADD COLUMN senderName TEXT NOT NULL DEFAULT ''")
            }
        }

        fun build(context: Context): MaseDatabase =
            Room.databaseBuilder(context, MaseDatabase::class.java, "mase_room.db")
                .addMigrations(MIGRATION_1_2, MIGRATION_2_3)
                .build()
    }
}
