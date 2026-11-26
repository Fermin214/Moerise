package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.CoreJson

internal class UsageOutbox(private val preferences: InstallPreferences) {
    private val day = 86_400_000L
    internal data class Attempt(val batch: UsageBatch, val remaining: List<UsageWindow>)

    fun initialize(now: Long) {
        if (preferences.read("last-attempt")?.toLongOrNull()?.let { it > 0 } != true)
            preferences.persist("last-attempt" to now.toString())
    }

    fun discard(vararg entries: Pair<String, String?>) {
        preferences.persist("aggregate" to null, "inflight-batch" to null, "usage-backlog" to null,
            "pending" to null, "in-flight" to null, *entries)
    }

    fun record(now: Long, identity: UsageBatch, row: UsageRow) {
        val stored = aggregate(now)
        val current = stored?.takeIf { it.batch.identity() == identity } ?: UsageWindow(now, identity)
        val rows = current.batch.rows.toMutableList()
        val index = rows.indexOfFirst { it.copy(count = 1) == row }
        if (index < 0) rows += row else rows[index] = rows[index].copy(count = rows[index].count + 1)
        val updated = current.copy(batch = identity.copy(rows = rows))
        val rollover = stored?.takeIf { it !== current }
        val entry = "aggregate" to CoreJson.encodeToString(updated)
        if (rollover == null) preferences.write(entry.first, entry.second)
        else preferences.writeBatch(entry, "usage-backlog" to encode(windows("usage-backlog", now) + rollover))
    }

    fun expire(now: Long) {
        val changes = buildList {
            if (aggregate(now) == null && preferences.read("aggregate") != null) add("aggregate" to null)
            for (key in listOf("inflight-batch", "usage-backlog")) {
                val original = preferences.read(key) ?: continue
                val valid = encode(windows(key, now))
                if (valid != original) add(key to valid)
            }
        }
        if (changes.isNotEmpty()) preferences.persist(*changes.toTypedArray())
    }

    fun ready(now: Long): Boolean {
        val attempt = preferences.read("last-attempt")?.toLongOrNull() ?: return false
        return now - attempt >= day && queued(now).isNotEmpty()
    }

    fun freeze(now: Long): Attempt? {
        if (!ready(now)) return null
        val windows = queued(now).sortedBy { it.start }
        val identity = windows.first().batch.identity()
        val sent = linkedMapOf<UsageRow, Int>()
        val remaining = windows.mapNotNull { window ->
            if (window.batch.identity() != identity) return@mapNotNull window
            val leftover = window.batch.rows.mapNotNull { row ->
                val key = row.copy(count = 1)
                val prior = sent[key] ?: 0
                val consumed = if (prior != 0 || sent.size < 256) minOf(row.count, 65_535 - prior) else 0
                if (consumed > 0) sent[key] = prior + consumed
                val rest = row.count - consumed
                if (rest > 0) row.copy(count = rest) else null
            }
            if (leftover.isEmpty()) null else window.copy(batch = identity.copy(rows = leftover))
        }
        preferences.persist("last-attempt" to now.toString(), "inflight-batch" to encode(windows),
            "aggregate" to null, "usage-backlog" to null)
        return Attempt(identity.copy(rows = sent.map { (row, count) -> row.copy(count = count) }), remaining)
    }

    fun acknowledge(now: Long, attempt: Attempt) {
        preferences.persist("inflight-batch" to encode(attempt.remaining.filter { it.valid(now) }))
    }

    private fun queued(now: Long) =
        windows("inflight-batch", now) + windows("usage-backlog", now) + listOfNotNull(aggregate(now))

    private fun windows(key: String, now: Long) = decode<List<UsageWindow>>(key).orEmpty().filter { it.valid(now) }

    private fun aggregate(now: Long) = decode<UsageWindow>("aggregate")?.takeIf { it.valid(now) }

    private fun UsageBatch.identity() = copy(rows = emptyList())

    private fun UsageWindow.valid(now: Long) = now - start in 0L until 2 * day && batch.rows.isNotEmpty()

    private fun encode(windows: List<UsageWindow>) = windows.takeIf { it.isNotEmpty() }?.let { CoreJson.encodeToString(it) }

    private inline fun <reified Value> decode(key: String): Value? = preferences.read(key)?.let {
        runCatching { CoreJson.decodeFromString<Value>(it) }.getOrNull()
    }

}
