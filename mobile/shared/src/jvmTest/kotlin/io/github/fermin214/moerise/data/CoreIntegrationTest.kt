package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.ktor.http.HttpMethod
import java.io.File
import java.util.UUID
import kotlin.test.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*

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
        val body = buildJsonObject { put("content", content) }.toString()
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
            assertEquals(accountKey, reopened.session().accountKey(origin))
            val replay = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Post, "/notes", body, key))
            assertEquals(noteName, replay.name)
            val id = replay.name.removePrefix("memos/")
            val read = CoreJson.decodeFromString<Note>(reopened.api(HttpMethod.Get, "/notes/$id"))
            assertEquals(content, read.content)
            val page = CoreJson.decodeFromString<NotePage>(reopened.api(HttpMethod.Get, "/notes?page_size=100&order=created_desc"))
            assertEquals(1, page.notes.count { it.name == read.name })
            val snapshot = reopened.api(HttpMethod.Get, "/map")
            assertEquals("disabled", CoreJson.parseToJsonElement(snapshot).jsonObject.getValue("status").jsonPrimitive.content)
            reopened.api(HttpMethod.Delete, "/notes/$id", buildJsonObject { put("source_hash", read.sourceHash) }.toString())
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
