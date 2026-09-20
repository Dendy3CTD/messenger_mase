package com.mase.messenger.data.local

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Guards the exported Room schemas (app/schemas, one JSON per database version).
 *
 * The build regenerates the file of the current version on every compile, so this test cannot
 * prove that the file is committed: that is checked in CI with `git diff --exit-code app/schemas`
 * after the build (roadmap, phase 5). What it does catch is a version that skips a number, which
 * would leave MigrationTestHelper without the schema of an intermediate version.
 * MigrationTestHelper tests start at 3 -> 4 (roadmap, phase 7).
 */
class RoomSchemaExportTest {
    private val schemaDir = File("schemas/com.mase.messenger.data.local.MaseDatabase")

    private fun currentVersion(): Int = Regex("""version\s*=\s*(\d+)""").find(
        File("src/main/java/com/mase/messenger/data/local/MaseDatabase.kt").readText()
    )!!.groupValues[1].toInt()

    @Test
    fun exportedVersionsAreContinuousUpToTheCurrentOne() {
        val versions = schemaDir.listFiles { f -> f.name.endsWith(".json") }!!
            .map { it.name.removeSuffix(".json").toInt() }.sorted()
        assertTrue("нет ни одной схемы в $schemaDir", versions.isNotEmpty())
        assertEquals("версии схем должны идти подряд: $versions", (versions.first()..versions.last()).toList(), versions)
        assertEquals("последняя схема должна быть для текущей версии БД", currentVersion(), versions.last())
    }

    @Test
    fun currentSchemaDescribesBothTables() {
        val text = File(schemaDir, "${currentVersion()}.json").readText()
        assertTrue(text.contains("\"tableName\": \"chats\""))
        assertTrue(text.contains("\"tableName\": \"messages\""))
        assertTrue(Regex("\"identityHash\": \"[0-9a-f]{32}\"").containsMatchIn(text))
    }
}
