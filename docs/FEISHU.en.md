# Feishu Integration Guide

[简体中文](FEISHU.md)

Moerise supports two Feishu modes:

- **Custom app bot**: Capture text, images, and files in a dedicated private chat, and send scheduled reviews with text and images.
- **Group webhook bot**: Send scheduled reviews to a Feishu group.

---

## 1. Prepare a Feishu App

For private capture and image reviews, create a custom app on the [Feishu Open Platform](https://open.feishu.cn/).

### 1. App Permissions

Enable these app scopes for `tenant_access_token`:

| Scope | Permission | Scenario | Purpose |
| --- | --- | --- | --- |
| `im:message.p2p_msg:readonly` | Read private messages sent to the bot | Capture | Receive text, images, and files |
| `im:message:send_as_bot` | Send as the app | Capture and delivery | Reply with save results and send reviews |
| `im:message:readonly` | Read private and group messages | Capture and delivery | Verify receipts and download received media |
| `im:resource` | Access and upload images/files | Image delivery | Upload review images and obtain `image_key` |

### 2. Event Subscription

1. Set the request URL to `https://your-domain.example/hooks/feishu`, routed to Core.
2. Subscribe to `im.message.receive_v1`.
3. If event encryption is enabled, copy its Encrypt Key into Moerise.
4. Create and publish an app version so scopes and subscriptions take effect.

---

## 2. Configuration

Configure **Settings → Messaging Channel** in Web, or initialize through JSON:

```json
{
  "channel": {
    "kind": "feishu-app",
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "verification_token": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "encrypt_key": null,
    "user_open_id": "ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "destination": null,
    "webhook_url": null,
    "webhook_secret": null
  },
  "notifications": {
    "enabled": true,
    "times": ["09:00", "18:00"]
  }
}
```

### Fields

| Field | Type | Description |
| --- | --- | --- |
| `kind` | string | `feishu-app` or `feishu-webhook` |
| `app_id` | string | App ID |
| `app_secret` | string | App Secret |
| `verification_token` | string | Callback verification token |
| `encrypt_key` | string / null | Event encryption key; `null` when disabled |
| `user_open_id` | string | Bound `ou_...` user allowed to capture notes and receive private reviews |
| `destination` | string / null | Must match `user_open_id` when specified; `null` uses the bound user |

> [!TIP]
> Environment variables include `MOERISE_FEISHU_APP_ID`, `MOERISE_FEISHU_APP_SECRET`, and `MOERISE_FEISHU_USER_OPEN_ID`. Deployment values initialize daily settings once; saved Web settings take precedence thereafter.

---

## 3. Private Note Capture

Send content directly to the bot in its private chat:

| Input | Memos Behavior | Reply |
| --- | --- | --- |
| **Text** | Preserve Markdown, newlines, spaces, and native tags, up to 64 KiB | Save result and original note link |
| **Image** | Download original bytes and create an image attachment note | Save result |
| **File** | Download and attach the original file, up to 8 MiB | Save result |

- **User boundary**: Only `user_open_id` can capture notes. Other users, groups, and the bot's own messages are ignored.
- **Capture semantics**: Private text is treated as note content, with no query, AI, or review command syntax.

---

## 4. Scheduled Reviews

At configured times, the bot sends that slot's selected notes:

1. **Order**:
   - One `「今日回顾」` header.
   - Each note's full text, followed by original images.
   - Tags and creation date, such as `#标签  创建于YYYY-MM-DD`; untagged notes show only the date.
2. **Long text**:
   - Split along newlines or UTF-8 boundaries within an **18 KiB encoded request budget**.
3. **Images**:
   - Fetch from Memos and upload to Feishu. Image-only notes send the image and footer.

---

## 5. Group Webhook Bot

For group review delivery, add a custom bot to the group and configure:

```json
{
  "channel": {
    "kind": "feishu-webhook",
    "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "webhook_secret": "optional-signing-secret"
  }
}
```

- No public callback is required; Core needs outbound HTTPS access.
- HMAC-SHA256 timestamp signing is supported.
- Webhook image delivery also requires `app_id` and `app_secret` for an app with `im:resource` upload permission.
