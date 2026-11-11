# Telegram review delivery

Moerise can deliver scheduled daily reviews through a Telegram bot. Each instance chooses one messaging channel: Telegram, a Feishu app bot, a Feishu webhook, or none. New installations start with no channel selected.

Telegram delivery includes the full note text, long text split into ordered messages, and images sent as original files. The title is `Daily review`; each note ends with its tags and creation date, such as `#ideas  Created on 2026-01-15`. Notes, tags and filenames keep their original language. Ordinary file attachments are skipped.

## Set up a bot

1. Open Telegram and find the official **@BotFather** account. Use `/newbot` to create a bot and obtain its Bot Token.
2. Open a private chat with your new bot and send `/start`. Bots cannot initiate a private conversation before the user contacts them.
3. Call the bot's `getUpdates` endpoint and find your message's `message.chat.id`. This is your numeric Chat ID.

For example, in Bash:

```bash
read -rs -p "Telegram Bot Token: " TELEGRAM_BOT_TOKEN
printf '\n'
curl --silent --show-error "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
unset TELEGRAM_BOT_TOKEN
```

Use a dedicated bot for Moerise. If `getUpdates` returns an empty list, send another message to the bot and try again. An existing webhook or another program consuming updates can prevent this setup step; see Telegram's [Bot features](https://core.telegram.org/bots/features) and [Bot API](https://core.telegram.org/bots/api#getupdates).

For a group, add the bot, send a command addressed to it, and use that update's `message.chat.id`, which is usually negative. All members of the destination chat can read the delivered notes. Chat usernames are not accepted.

## Configure Moerise

Open **Settings → Messaging channel**:

1. Choose **Telegram**.
2. Replace **Telegram Bot Token** with the token from BotFather.
3. Enter the numeric Chat ID in **Delivery destination**.
4. Turn on **Enable review delivery**, choose the times and timezone, and save.

The token is encrypted in product settings and is never returned to the browser. Text and image capabilities update after saving. Telegram supports outbound review delivery; note capture through Telegram is not provided.

The Core process needs outbound HTTPS access to `api.telegram.org`. No public callback endpoint, polling worker, extra container or additional dependency is required. Telegram delivery continues when the browser is closed.

For initial deployment, the corresponding environment variables are:

```dotenv
MOERISE_CHANNEL_KIND=telegram
MOERISE_TELEGRAM_BOT_TOKEN=
MOERISE_TELEGRAM_CHAT_ID=
MOERISE_NOTIFICATIONS_ENABLED=true
MOERISE_NOTIFICATIONS_TIMES=09:00,18:00
MOERISE_TIMEZONE=Europe/London
```

In JSON, use `channel.kind: "telegram"`, `channel.bot_token` and `channel.destination`. Environment and JSON values initialize product settings once; after that, change the channel in Web settings. See [product settings](SETTINGS.md).

## Delivery and recovery

Text messages stay within 4,096 UTF-16 units and preserve the full original content across parts. Moerise sends plain text without Telegram Markdown parsing or automatic link previews. Images use `sendDocument` to preserve the source file bytes and support image-only notes, with a 10 MiB limit per image. Telegram may present these images as downloadable files.

Each part has its own persistent status and receipt. Later parts wait for the previous one to finish. Rate limits follow Telegram's `retry_after`, with a fallback wait when that field is absent. An explicit rejection remains queued for retry.

Timeouts, server errors and untrusted success responses leave the part in `unknown`. Telegram does not provide a general Bot API method for querying a sent message by its receipt. Check the destination chat, then confirm delivery through the [notification management API](NOTIFICATIONS.md#查询与处理), omitting `receipt`:

```json
{"decision":"sent"}
```

If the part did not arrive, confirm `{"decision":"not_sent"}` to queue a new attempt. Automatic sending does not replay an unknown part.

Changing the selected channel applies to newly queued reviews. Unfinished notifications keep their original bot and chat; the necessary channel configuration remains encrypted across restarts. Selecting no channel turns off scheduled delivery in the settings form. Daily review remains available in the Web app.
