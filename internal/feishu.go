package moerise

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

var feishuIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type feishuEvent struct {
	Type      string `json:"type"`
	Token     string `json:"token"`
	Challenge string `json:"challenge"`
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		AppID     string `json:"app_id"`
		Token     string `json:"token"`
	} `json:"header"`
	Event struct {
		Sender struct {
			Type string `json:"sender_type"`
			ID   struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			ID         string `json:"message_id"`
			Chat       string `json:"chat_type"`
			Type       string `json:"message_type"`
			Content    string `json:"content"`
			CreateTime string `json:"create_time"`
		} `json:"message"`
	} `json:"event"`
}

type feishuMessage struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type FeishuPlatform interface {
	Reply(context.Context, string, string, string) (string, error)
	Download(context.Context, string, string, string, string) (CreateAttachment, error)
	SendText(context.Context, string, string, string) (string, error)
	GetMessage(context.Context, string) (FeishuSentMessage, error)
}

type FeishuService struct {
	Store         *Store
	Source        Source
	Records       *RecordService
	Client        FeishuPlatform
	Notifications *NotificationService
}

func NewFeishuService(store *Store, source Source, records *RecordService, notifications *NotificationService) *FeishuService {
	return newFeishuService(store.Settings.Channel, store, source, records, notifications)
}

func newFeishuService(config ChannelConfig, store *Store, source Source, records *RecordService, notifications *NotificationService) *FeishuService {
	if configured(config.AppID) == "" || configured(config.AppSecret) == "" {
		return nil
	}
	return &FeishuService{Store: store, Source: source, Records: records,
		Client:        NewFeishuClient(config),
		Notifications: notifications}
}

func (s *FeishuService) Receive(request *http.Request) (any, int, error) {
	config := s.Store.Settings.Channel
	if config.feishuMode() != "callback" || configured(config.VerificationToken) == "" {
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	if request.Method != "POST" {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	body, err := s.eventJSON(request)
	if err != nil {
		return nil, 0, err
	}
	var event feishuEvent
	if json.Unmarshal(body, &event) != nil {
		return nil, 0, failure("INVALID_ARGUMENT", "Invalid Feishu event", 422, false)
	}
	token := event.Header.Token
	if event.Type == "url_verification" {
		token = event.Token
	}
	if !hmac.Equal([]byte(token), []byte(configured(config.VerificationToken))) {
		return nil, 0, failure("UNAUTHENTICATED", "Invalid Feishu verification token", 401, false)
	}
	if event.Type == "url_verification" {
		if event.Challenge == "" {
			return nil, 0, failure("INVALID_ARGUMENT", "Missing Feishu challenge", 422, false)
		}
		s.Store.setBotStatus("feishu-app", configured(config.AppID), "connected", "", "")
		return map[string]string{"challenge": event.Challenge}, 200, nil
	}
	accepted, err := s.receiveEvent(event)
	if err != nil {
		return nil, 0, err
	}
	if accepted {
		s.Store.setBotStatus("feishu-app", configured(config.AppID), "connected", "", "")
	}
	return map[string]int{"code": 0}, 200, nil
}

func (s *FeishuService) receiveEvent(event feishuEvent) (bool, error) {
	config := s.Store.Settings.Channel
	if event.Header.AppID != configured(config.AppID) {
		return false, failure("UNAUTHENTICATED", "Feishu application differs from the binding", 401, false)
	}
	peer := event.Event.Sender.ID.OpenID
	owner := configured(config.UserOpenID)
	if event.Header.EventType != "im.message.receive_v1" || event.Event.Sender.Type != "user" || event.Event.Message.Chat != "p2p" ||
		!feishuIDPattern.MatchString(peer) || owner != "" && peer != owner {
		return false, nil
	}
	message := feishuMessage{event.Event.Message.ID, event.Event.Message.Type, event.Event.Message.Content}
	if !feishuIDPattern.MatchString(message.ID) || !feishuIDPattern.MatchString(event.Header.EventID) {
		return false, failure("INVALID_ARGUMENT", "Invalid Feishu event or message identifier", 422, false)
	}
	payload, _ := json.Marshal(message)
	hash := hashBytes(payload)
	sealed, err := s.seal(message.ID, peer, payload)
	if err != nil {
		return false, err
	}
	now := time.Now()
	at := now
	if millis, err := strconv.ParseInt(event.Event.Message.CreateTime, 10, 64); err == nil && millis > 0 {
		at = time.UnixMilli(millis)
	}
	accepted := false
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		var restored int64
		if err := tx.QueryRow(`SELECT COALESCE((SELECT restored_at FROM bot_restore WHERE instance_id=?),0)`,
			s.Store.Settings.Memos.InstanceID).Scan(&restored); err != nil || at.UnixMilli() <= restored {
			return err
		}
		if owner == "" {
			bound, err := s.Store.bindBotPeer(tx, "feishu-app", event.Header.AppID, peer, now)
			if err != nil || bound != peer {
				return err
			}
		}
		accepted = true
		var saved string
		err := tx.QueryRow("SELECT event_hash FROM channel_events WHERE message_id = ?", message.ID).Scan(&saved)
		if err == nil {
			if saved != hash {
				return failure("IDEMPOTENCY_CONFLICT", "Feishu message payload changed", 409, false)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(`INSERT INTO channel_events
			(message_id, event_id, event_hash, app_id, sender_open_id, payload, status, reply_uuid, available_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`, message.ID, event.Header.EventID, hash,
			configured(config.AppID), peer, sealed, newID(), nowSeconds(), at.UTC().Format(time.RFC3339Nano))
		return err
	})
	return accepted && err == nil, err
}

func (s *Store) feishuOwner() (string, error) {
	channel := s.Settings.Channel
	if owner := configured(channel.UserOpenID); owner != "" {
		return owner, nil
	}
	owner, _, err := s.botPeer("feishu-app", configured(channel.AppID))
	return owner, err
}

func (s *FeishuService) prepare() error {
	config := s.Store.Settings.Channel
	owner, err := s.Store.feishuOwner()
	if err != nil {
		return err
	}
	if _, err := s.Store.state.Exec(`UPDATE channel_events SET status = 'pending' WHERE status = 'processing'
		AND app_id = ? AND sender_open_id = ?`, configured(config.AppID), owner); err != nil {
		return err
	}
	rows, err := s.Store.state.Query(`SELECT message_id FROM channel_events WHERE status = 'processed' AND result_kind IS NOT NULL
		AND (reply_status = 'pending' OR (reply_status = 'unknown' AND last_error = 'STATE_RESTORED'
		AND NOT EXISTS (SELECT 1 FROM notifications WHERE source_event = message_id)))
		AND app_id = ? AND sender_open_id = ?`,
		configured(config.AppID), owner)
	if err != nil {
		return err
	}
	adopt := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		adopt = append(adopt, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range adopt {
		if _, err := s.Notifications.EnqueueReply(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *FeishuService) processOne(ctx context.Context) (bool, error) {
	config := s.Store.Settings.Channel
	owner, err := s.Store.feishuOwner()
	if err != nil || owner == "" {
		return false, err
	}
	var messageID, status, kind, resource string
	var payload []byte
	var attempts int
	var received string
	err = s.Store.state.QueryRow(`SELECT message_id, status, COALESCE(result_kind, ''),
		COALESCE(resource_name, ''), payload, attempts, created_at FROM channel_events
		WHERE (status = 'pending' OR status = 'processed' AND reply_status = 'pending') AND available_at <= ?
		AND app_id = ? AND sender_open_id = ? ORDER BY available_at, created_at LIMIT 1`,
		nowSeconds(), configured(config.AppID), owner).
		Scan(&messageID, &status, &kind, &resource, &payload, &attempts, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status == "pending" {
		if _, err := s.Store.state.Exec("UPDATE channel_events SET status = 'processing' WHERE message_id = ?", messageID); err != nil {
			return false, err
		}
		var body []byte
		body, err = s.unseal(messageID, owner, payload)
		var message feishuMessage
		if err == nil {
			err = json.Unmarshal(body, &message)
		}
		if err == nil {
			var at time.Time
			at, err = time.Parse(time.RFC3339Nano, received)
			if err == nil {
				kind, resource, err = s.execute(ctx, message, at)
			}
		}
		if err != nil {
			var service *ServiceError
			if errors.As(err, &service) && !service.Retryable {
				kind, resource = "error", service.Code
				if service.Code == "INVALID_ARGUMENT" {
					switch message.Type {
					case "text":
						kind = "invalid-text"
					case "image", "file":
						kind = "invalid-attachment"
					}
				}
			} else {
				return true, s.retry(messageID, "pending", attempts, errorCode(err))
			}
		}
		if _, err := s.Store.state.Exec(`UPDATE channel_events SET status = 'processed', result_kind = ?,
			resource_name = ?, payload = NULL, attempts = attempts + 1,
			last_error = CASE WHEN reply_status = 'unknown' THEN last_error ELSE NULL END
			WHERE message_id = ?`, kind, resource, messageID); err != nil {
			return false, err
		}
	}
	_, err = s.Notifications.EnqueueReply(messageID)
	return true, err
}

func (s *FeishuService) retry(id, status string, attempts int, code string) error {
	slog.Warn("feishu event deferred", "message_id", id, "code", code)
	_, err := s.Store.state.Exec(`UPDATE channel_events SET status = ?, attempts = attempts + 1,
		available_at = ?, last_error = ? WHERE message_id = ?`,
		status, nowSeconds()+float64(int64(1)<<min(attempts+1, 8)), code, id)
	return err
}

const feishuHelp = "直接发送文字或图片即可记录笔记，也支持文件消息。"

func (s *FeishuService) execute(ctx context.Context, message feishuMessage, at time.Time) (string, string, error) {
	key := "feishu:" + hashBytes([]byte(message.ID))
	var content struct {
		Text     string `json:"text"`
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		Filename string `json:"file_name"`
	}
	if json.Unmarshal([]byte(message.Content), &content) != nil {
		return "", "", failure("INVALID_ARGUMENT", "Invalid Feishu message content", 422, false)
	}
	if message.Type == "file" || message.Type == "image" {
		fileKey := content.FileKey
		if message.Type == "image" {
			fileKey = content.ImageKey
		}
		if !feishuIDPattern.MatchString(fileKey) {
			return "", "", failure("INVALID_ARGUMENT", "Invalid Feishu file identifier", 422, false)
		}
		file, err := s.Client.Download(ctx, message.ID, fileKey, message.Type, content.Filename)
		if err != nil {
			return "", "", err
		}
		input := CreateNote{Attachments: []CreateAttachment{file}}
		note, _, err := s.Records.Create(ctx, key, input)
		return "saved", note.Name, err
	}
	if message.Type != "text" {
		return "help", "", nil
	}
	command, body := botTextCommand(content.Text)
	if command == "review" {
		batch, err := s.Notifications.Reviews.Daily(ctx, at)
		if err != nil {
			return "review-unavailable", "", nil
		}
		return "review", batch.ID, nil
	}
	if command == "invalid" {
		return "", "", failure("INVALID_ARGUMENT", "Send 1 to 65536 bytes of text", 422, false)
	}
	note, _, err := s.Records.Create(ctx, key, CreateNote{Content: body})
	return "saved", note.Name, err
}

func (s *FeishuService) render(ctx context.Context, kind, resource string) (string, error) {
	switch kind {
	case "review-unavailable":
		return "暂时读不到笔记", nil
	case "review":
		batch, err := s.Notifications.Reviews.Get(ctx, resource)
		if err != nil {
			return "暂时读不到笔记", nil
		}
		return botReviewText(batch), nil
	case "help":
		return feishuHelp, nil
	case "invalid-text":
		return "请发送非空正文，最多 64 KiB。", nil
	case "invalid-attachment":
		return "请检查附件的文件名和格式。附件不能为空，最大 8 MiB。", nil
	case "error":
		return "操作失败：" + resource + "。请检查参数后重新发送。", nil
	case "saved":
		note, err := NewNoteService(s.Store.Settings, s.Store, s.Source).Get(ctx, resource)
		if errorCode(err) == "SOURCE_NOT_FOUND" {
			return "笔记已删除或不可访问。", nil
		}
		if err != nil {
			return "", err
		}
		return "已保存\n" + note.Name + "\n" + note.URL, nil
	default:
		return "", fmt.Errorf("invalid Feishu event result")
	}
}
