package io.github.fermin214.moerise.presentation

import java.util.concurrent.ConcurrentHashMap
import io.github.fermin214.moerise.data.CoreJson
import kotlin.test.*

internal class MemoryInstallPreferences : InstallPreferences {
    private val values = ConcurrentHashMap<String, String>()
    override val clientVersion = "0.3.0"
    override val systemMajor = "29"
    override fun read(key: String) = values[key]
    override fun write(key: String, value: String?) {
        if (value == null) values.remove(key) else values[key] = value
    }
}

class UsageOutboxTest {
    private val day = 86_400_000L
    private val start = 10 * day
    private val identity = UsageBatch(clientVersion = "0.3.0", coreVersion = "0.3.0", systemMajor = "29", rows = emptyList())
    private fun row(operation: String) = UsageRow(operation, "success", "unknown", "user", 1)

    @Test
    fun firstWindowAndRestartRespectThePersistedAttemptAndKeepNewEventsAfterAcknowledgement() {
        val preferences = MemoryInstallPreferences()
        val outbox = UsageOutbox(preferences)
        outbox.initialize(start)
        outbox.record(start, identity, row("sync"))
        assertNull(outbox.freeze(start + day - 1))
        val attempt = start + day
        val frozen = assertNotNull(outbox.freeze(attempt))
        assertEquals(listOf(row("sync")), frozen.batch.rows)
        assertEquals(attempt.toString(), preferences.read("last-attempt"))
        assertNotNull(preferences.read("inflight-batch"))

        val resumed = UsageOutbox(preferences)
        resumed.initialize(attempt + 10)
        resumed.record(attempt + 10, identity, row("edit"))
        assertNull(resumed.freeze(attempt + 10))
        assertEquals(attempt.toString(), preferences.read("last-attempt"))
        resumed.acknowledge(attempt + 10, frozen)
        assertNull(preferences.read("inflight-batch"))
        assertEquals(listOf(row("edit")), resumed.freeze(attempt + day)?.batch?.rows)
    }

    @Test
    fun failureMergesAtTheNextAttemptWithoutExtendingEitherWindowsLifetime() {
        val preferences = MemoryInstallPreferences()
        val outbox = UsageOutbox(preferences)
        val attempt = start + day
        outbox.initialize(start)
        outbox.record(start + 1, identity, row("sync"))
        assertNotNull(outbox.freeze(attempt))
        outbox.record(attempt + 1, identity, row("edit"))
        assertNull(outbox.freeze(attempt + 5_001))
        assertEquals(listOf(row("sync"), row("edit")), outbox.freeze(attempt + day)?.batch?.rows)
        assertEquals(listOf(row("edit")), outbox.freeze(attempt + day * 2)?.batch?.rows)
        outbox.expire(attempt + day * 2 + 1)
        assertNull(preferences.read("inflight-batch"))
    }

    @Test
    fun withdrawalClearsActiveFrozenAndPendingData() {
        val preferences = MemoryInstallPreferences()
        val outbox = UsageOutbox(preferences)
        outbox.initialize(start)
        outbox.record(start, identity, row("sync"))
        outbox.freeze(start + day)
        outbox.record(start + day, identity, row("edit"))
        preferences.write("pending", "{}")
        preferences.write("in-flight", "local")

        outbox.discard("choice-v1" to "off", "last-attempt" to null)
        for (key in listOf("aggregate", "inflight-batch", "usage-backlog", "pending", "in-flight", "last-attempt")) {
            assertNull(preferences.read(key), key)
        }
        assertEquals("off", preferences.read("choice-v1"))
    }

    @Test
    fun upgradeRetriesWithTheOriginalVersionsAndPreservesTheNewerWindow() {
        val preferences = MemoryInstallPreferences()
        val outbox = UsageOutbox(preferences)
        outbox.initialize(start)
        val sentAt = start + day
        outbox.record(start + 1, identity, row("sync"))
        assertEquals(identity.clientVersion, outbox.freeze(sentAt)?.batch?.clientVersion)
        val upgradedClient = identity.copy(clientVersion = "0.4.0")
        val upgradedCore = upgradedClient.copy(coreVersion = "0.4.0")
        val reopened = UsageOutbox(preferences)
        reopened.record(sentAt + 1, upgradedClient, row("edit"))
        reopened.record(sentAt + 2, upgradedCore, row("create"))
        reopened.expire(sentAt + 2)
        val retryAt = sentAt + day
        val retry = assertNotNull(reopened.freeze(retryAt))
        assertEquals(identity.copy(rows = listOf(row("sync"))), retry.batch)
        reopened.acknowledge(retryAt, retry)
        assertEquals(listOf(sentAt + 1, sentAt + 2), retry.remaining.map { it.start })
        assertEquals(upgradedClient.copy(rows = listOf(row("edit"))), reopened.freeze(retryAt + day)?.batch)
    }

    @Test
    fun acknowledgementRetainsUnsentRowsAndCountsInTheirOriginalWindow() {
        val preferences = MemoryInstallPreferences()
        val outbox = UsageOutbox(preferences)
        val unique = (1..255).map { row("operation-$it") }
        val old = UsageWindow(start + 1, identity.copy(rows = listOf(row("sync").copy(count = 65_530)) + unique))
        val recent = UsageWindow(start + day + 1,
            identity.copy(rows = listOf(row("sync").copy(count = 10), row("extra"))))
        preferences.write("last-attempt", (start + day).toString())
        preferences.write("inflight-batch", CoreJson.encodeToString(listOf(old)))
        preferences.write("aggregate", CoreJson.encodeToString(recent))
        val retryAt = start + day * 2
        val attempt = assertNotNull(outbox.freeze(retryAt))
        assertEquals(256, attempt.batch.rows.size)
        assertEquals(65_535, attempt.batch.rows.first().count)
        outbox.acknowledge(retryAt, attempt)
        val remaining = CoreJson.decodeFromString<List<UsageWindow>>(assertNotNull(preferences.read("inflight-batch"))).single()
        assertEquals(recent.start, remaining.start)
        assertEquals(listOf(row("sync").copy(count = 5), row("extra")), remaining.batch.rows)
        assertEquals(remaining.batch, outbox.freeze(retryAt + day)?.batch)
    }
}
