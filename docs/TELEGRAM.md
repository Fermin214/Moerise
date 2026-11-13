# Telegram Review Delivery

Moerise supports scheduled daily review delivery through an official Telegram Bot. Each Moerise instance can configure one active messaging channel: Telegram, a Feishu App bot, a Feishu webhook, or none.

When enabled, Moerise delivers full note text (split into ordered messages if necessary) and images as original files. The delivery header is titled `Daily review`, and each note concludes with its tags and creation date (e.g., `#ideas  Created on 2026-01-15`). Note content, tags, and file attachments retain their original languages.

---

## 1. Create and Configure a Telegram Bot

### Step 1: Create Your Bot
1. In Telegram, start a chat with the official **@BotFather**.
2. Send `/newbot` and follow the prompts to choose a display name and username.
3. Save the generated **Bot Token** securely.

### Step 2: Initialize Chat
1. Open a direct chat with your newly created bot and send `/start`. Telegram bots cannot initiate direct messages with users until the user contacts them first.

### Step 3: Find Your Chat ID
Retrieve your numeric Telegram Chat ID using the `getUpdates` API endpoint:

```bash
read -rs -p "Telegram Bot Token: " TELEGRAM_BOT_TOKEN
printf '\n'
curl --silent --show-error "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
unset TELEGRAM_BOT_TOKEN
```

Look for `message.chat.id` in the returned JSON. For a private chat, this is a positive integer (e.g. `123456789`).

- **For Groups**: Add your bot to the group, send a message mentioning the bot, and locate `message.chat.id` (usually a negative number).
- **For Channels**: Add your bot as an administrator with posting rights, send a channel post, and locate `channel_post.chat.id`.

---

## 2. Configure Moerise

### Web UI Configuration
Navigate to **Settings → Messaging Channel**:
1. Select **Telegram**.
2. Enter your **Telegram Bot Token**.
3. Set **Delivery Destination** to your numeric Chat ID.
4. Enable **Enable review delivery**, specify desired delivery times and timezone, and click **Save**.

The Bot Token is encrypted inside `state.sqlite3` and is never exposed back to the browser.

### Environment Variable Configuration (Docker Compose)
For automated setup via `.env`:

```dotenv
MOERISE_CHANNEL_KIND=telegram
MOERISE_TELEGRAM_BOT_TOKEN=123456789:ABCdefGHIjklMNOpqrSTUvwxYZ
MOERISE_TELEGRAM_CHAT_ID=123456789
MOERISE_NOTIFICATIONS_ENABLED=true
MOERISE_NOTIFICATIONS_TIMES=09:00,18:00
MOERISE_TIMEZONE=Asia/Shanghai
```

Deployment values initialize daily settings once; saved Web settings take precedence thereafter.

---

## 3. Delivery Specifications & Format

1. **Text Chunking**:
   - Telegram enforces a limit of 4,096 UTF-16 units per message.
   - Moerise splits long notes along paragraph breaks or line breaks while preserving UTF-8 integrity.
   - Text is sent as plain text without risky Markdown parsing or automatic link previews to avoid formatting errors.
2. **Whitespace and Invisible Character Preservation**:
   - If a chunk consists solely of whitespace or invisible characters, Moerise automatically sends it as a `.txt` file attachment to prevent Telegram from rejecting the message as empty.
3. **Images as Original Documents**:
   - Images are uploaded using `sendDocument` (up to 10 MiB per image) to guarantee that image bytes and metadata remain uncompressed.
4. **Rate Limiting & Retries**:
   - Moerise obeys the Bot API response's `parameters.retry_after` value on HTTP 429 and maintains persistent cooldowns across restarts.
   - If a message transmission times out or its status cannot be confirmed, it enters the `unknown` state. You can inspect and resolve deliveries via the [Notification Management API](NOTIFICATIONS.md).

Telegram normalizes surrounding whitespace in plain text messages. Moerise preserves the submitted text and uses files for chunks that Telegram would treat as empty. Telegram has no general API for retrieving an arbitrary sent message by ID, so `unknown` deliveries require you to inspect the destination chat before resolving them.
