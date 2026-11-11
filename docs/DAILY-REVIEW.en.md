# Moerise Daily Review

[简体中文](DAILY-REVIEW.md)

Daily Review brings earlier thoughts, excerpts, and notes back into everyday reading, making it easier to rediscover old questions and connections across time.

---

## 1. Low-Pressure Reading

Moerise avoids the counters and checklists often attached to review systems:

- **No unread badges**: No remaining-note count.
- **No completion marks**: Read, skip, or glance without confirming an action or maintaining a streak.
- **Fixed presentation**: A saved full-day selection is available in Web and messaging channels without changing on refresh.

---

## 2. Selection & Deduplication

| Parameter | Default | Range | Behavior |
| --- | --- | --- | --- |
| `count` | `3` | 1–20 | Notes allocated per delivery slot |
| `min_age_hours` | `72` | 0–87600 | Minimum time since note creation |
| `recent_days` | `7` | 0–3650 | Exclude notes selected in recent local days |
| `include_archived` | `false` | true / false | Include archived notes in the candidate pool |

### Selection Flow

1. **Filter**: Match the bound author, maturity delay, and recent-selection exclusion.
2. **Content eligibility**: Notes need text or an image. Attachment-only notes containing PDF, ZIP, or other non-image files do not use review slots.
3. **Equal-probability selection**: Shuffle eligible candidates and select the required count.
4. **Full-day allocation**:
   - Required notes = number of slots × `count`.
   - With `09:00`, `18:00`, and `count=3`, select six distinct notes: three for each slot.
   - Earlier slots fill first when candidates are scarce. An empty pool sends no notification.

---

## 3. Fixed Plans & Delivery

1. **Persistent daily plan**:
   - The first successful background scheduling pass of the local day creates the plan in `state.sqlite3`; the first Web read can also create it. Source failures are retried later.
   - Restart, refresh, and settings changes do not redraw the selection. Rules, timezone, and times apply on the next local day. Reading still verifies live Memos content.
2. **Reading and delivery**:
   - **Web**: Read all selected notes with Markdown, images, attachment links, and detail navigation.
   - **Telegram**: Scheduled delivery to a chat or group with tags and creation dates.
   - **Feishu bot/webhook**: Scheduled private or group delivery with ordered long-text chunks and images.
3. **Downtime recovery**:
   - Recovery creates a delivery task only for the most recent missed slot of the same day. Existing queued tasks recover independently; other selections remain available in that day's Web plan.
