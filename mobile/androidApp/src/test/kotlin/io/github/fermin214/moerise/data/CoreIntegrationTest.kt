package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.github.fermin214.moerise.presentation.NoteDetailController
import io.github.fermin214.moerise.presentation.ReviewBatch
import io.ktor.http.HttpMethod
import java.io.File
import java.util.UUID
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class CoreIntegrationTest {
    @Test
    fun realCoreAndMemosSessionReopenIdempotencyAndLogout() = runBlocking<Unit> {
        val fixturePath = requireNotNull(System.getenv("MOERISE_ANDROID_FIXTURE")) { "设置 MOERISE_ANDROID_FIXTURE 为隔离环境连接档案" }
        val fixtureFile = File(fixturePath)
        val fixture = CoreJson.parseToJsonElement(fixtureFile.readText()).jsonObject
        fun field(name: String) = fixture.getValue(name).jsonPrimitive.content
        val origin = normalizeOrigin(field("origin"), true)
        val folder = File(fixtureFile.parentFile, "client-secrets").apply { mkdirs() }
        val secrets = object : SecretStore {
            override suspend fun read(key: String): String? = File(folder, key.hashCode().toString()).takeIf(File::exists)?.readText()
            override suspend fun write(key: String, value: String?) {
                val file = File(folder, key.hashCode().toString())
                if (value == null) file.delete() else file.writeText(value)
            }
        }
        val content = "Android session integration ${UUID.randomUUID()} #android"
        val fileBytes = ByteArray(2_852_127) { (it % 251).toByte() }
        val body = buildJsonObject {
            put("content", content)
            put("attachments", buildJsonArray { add(buildJsonObject {
                put("filename", "android.mp4"); put("type", "video/mp4")
                put("content", java.util.Base64.getEncoder().encodeToString(fileBytes))
            }) })
        }.toString()
        val key = UUID.randomUUID().toString()
        var noteName = ""
        val first = CoreClient(origin, SessionCookies(origin, secrets))
        val accountKey: String
        try {
            assertEquals("ready", first.capabilities().notes)
            accountKey = first.login(field("username"), field("password")).accountKey(origin)
            val created = CoreJson.decodeFromString<Note>(first.api(HttpMethod.Post, "/notes", body, key))
            noteName = created.name
            assertEquals(content, created.content)
        } finally { first.close() }
        val reopened = CoreClient(origin, SessionCookies(origin, secrets))
        try {
            reopened.bindAccount(accountKey)
            val restoredIdentity = reopened.session()
            assertEquals(accountKey, restoredIdentity.accountKey(origin))
            val replay = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Post, "/notes", body, key))
            assertEquals(noteName, replay.name)
            val id = replay.name.removePrefix("memos/")
            val read = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Get, "/notes/$id"))
            assertEquals(content, read.content)
            assertContentEquals(fileBytes, reopened.attachmentBytes(read, read.attachments.single(), 8 * 1024 * 1024))
            val patchKey = UUID.randomUUID().toString()
            val patchBody = buildJsonObject {
                put("source_hash", read.sourceHash); put("content", "$content edited")
                put("remove_attachments", buildJsonArray { add(read.attachments.single().getValue("name")) })
                put("attachments", buildJsonArray { add(buildJsonObject {
                    put("filename", "edited.txt"); put("type", "text/plain"); put("content", "ZWRpdGVk")
                }) })
            }.toString()
            val edited = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Patch, "/notes/$id", patchBody, patchKey))
            val replayEdit = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Patch, "/notes/$id", patchBody, patchKey))
            assertEquals(edited, replayEdit)
            assertEquals("$content edited", edited.content)
            assertEquals(1, edited.attachments.size)
            assertEquals("edited.txt", edited.attachments.single().getValue("filename").jsonPrimitive.content)
            assertContentEquals("edited".encodeToByteArray(), reopened.attachmentBytes(edited, edited.attachments.single(), 1024))
            var live = edited
            for (state in listOf("ARCHIVED", "NORMAL")) {
                live = CoreJson.decodeFromString(reopened.api(HttpMethod.Patch, "/notes/$id", buildJsonObject {
                    put("source_hash", live.sourceHash); put("state", state)
                }.toString(), UUID.randomUUID().toString()))
                assertEquals(state, live.state)
                if (state == "ARCHIVED") assertEquals("WALK_ARCHIVED", assertFailsWith<CoreException> {
                    reopened.api(HttpMethod.Post, "/walks/start", buildJsonObject { put("name", live.name) }.toString())
                }.detail.code)
            }
            val page = CoreJson.decodeFromString<NotePage>(reopened.api(HttpMethod.Get, "/notes?page_size=100&order=created_desc"))
            assertEquals(1, page.notes.count { it.name == read.name })
            val date = java.time.Instant.parse(live.createTime).atZone(java.time.ZoneId.of(restoredIdentity.timezone)).toLocalDate().toString()
            val month = date.substring(0, 7)
            val tags = CoreJson.parseToJsonElement(reopened.api(HttpMethod.Get, "/tags?state=NORMAL")).jsonObject.getValue("tags").jsonArray
            assertTrue(tags.any { it.jsonObject.getValue("tag").jsonPrimitive.content == "android" })
            val calendar = CoreJson.parseToJsonElement(reopened.api(HttpMethod.Get, "/calendar?state=NORMAL&month=$month")).jsonObject
            assertEquals(month, calendar.getValue("month").jsonPrimitive.content)
            assertTrue(calendar.getValue("days").jsonArray.any { it.jsonObject.getValue("date").jsonPrimitive.content == date })
            for (mode in listOf("keyword", "hybrid")) {
                val search = CoreJson.parseToJsonElement(reopened.api(HttpMethod.Post, "/search", buildJsonObject {
                    put("query", content.substringAfter("integration ").substringBefore(" #")); put("mode", mode)
                    put("state", "NORMAL"); put("date", date); put("tags", buildJsonArray { add("android") }); put("page_size", 20)
                }.toString())).jsonObject
                assertEquals("keyword", search.getValue("mode").jsonPrimitive.content)
                assertFalse(search.getValue("semantic").jsonObject.getValue("used").jsonPrimitive.boolean)
                assertEquals(live.name, search.getValue("notes").jsonArray.single().jsonObject.getValue("name").jsonPrimitive.content)
            }
            val snapshot = reopened.api(HttpMethod.Get, "/map")
            assertEquals("disabled", CoreJson.parseToJsonElement(snapshot).jsonObject.getValue("status").jsonPrimitive.content)
            val review = CoreJson.decodeFromString<ReviewBatch>(reopened.api(HttpMethod.Get, "/reviews/daily"))
            assertEquals(restoredIdentity.timezone, review.timezone)
            assertEquals(java.time.LocalDate.now(java.time.ZoneId.of(review.timezone)).toString(), review.date)
            assertTrue(review.items.all { it.unavailable || (it.note?.name == it.name && it.note.instanceId == live.instanceId) })
            assertEquals(review.id, CoreJson.decodeFromString<ReviewBatch>(reopened.api(HttpMethod.Get, "/reviews/daily")).id)
            for (walkBody in listOf("{}", buildJsonObject { put("name", live.name) }.toString())) {
                assertEquals("EMBEDDING_DISABLED", assertFailsWith<CoreException> {
                    reopened.api(HttpMethod.Post, "/walks/start", walkBody)
                }.detail.code)
            }
            checkLiveChat(reopened, live, File(field("modelStats")))
            val db = testDatabase(File(folder, "actions.sqlite3").absolutePath)
            try {
                coroutineScope {
                    var deletionConfirmed = false
                    val repository = NoteRepository(db)
                    repository.cache(accountKey, NotePage(listOf(live)))
                    val actions = NoteDetailController({ repository }, { accountKey }, { reopened }, this) { _, _, updated ->
                        assertNull(updated)
                        deletionConfirmed = true
                    }
                    actions.delete(live)
                    val deleted = withTimeout(30_000) { actions.state.first { !it.busy } }
                    assertNull(deleted.error, "删除动作失败：$deleted")
                    assertTrue(deletionConfirmed, "删除动作未通知列表刷新")
                    assertTrue(repository.snapshot(accountKey)?.notes?.isEmpty() == true)
                    assertNull(repository.action(accountKey, live.name))
                    assertEquals(404, assertFailsWith<CoreException> {
                        reopened.api(HttpMethod.Get, "/notes/$id")
                    }.status)
                }
            } finally { db.close() }
            val oldSession = secrets.read("session:$origin")
            reopened.logout()
            assertNull(secrets.read("session:$origin"))
            secrets.write("session:$origin", oldSession)
        } finally { reopened.close() }
        try {
            CoreClient(origin, SessionCookies(origin, secrets)).use {
                assertEquals(401, assertFailsWith<CoreException> { it.session() }.status)
            }
        } finally { folder.deleteRecursively() }
    }
}
