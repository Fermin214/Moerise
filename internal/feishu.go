package moerise

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
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
			ID      string `json:"message_id"`
			Chat    string `json:"chat_type"`
			Type    string `json:"message_type"`
			Content string `json:"content"`
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
	Reviews       *ReviewService
	Search        *SearchService
	Client        FeishuPlatform
	Notifications *NotificationService
	wake          chan struct{}
}

func NewFeishuService(store *Store, source Source, records *RecordService, reviews *ReviewService, notifications *NotificationService) *FeishuService {
	if configured(store.Settings.Channel.AppID) == "" || store.Settings.Channel.Kind != "feishu-app" {
		return nil
	}
	return &FeishuService{Store: store, Source: source, Records: records, Reviews: reviews,
		Search: NewSearchService(store, source, nil), Client: NewFeishuClient(store.Settings.Channel),
		Notifications: notifications, wake: make(chan struct{}, 1)}
}

func (s *FeishuService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *FeishuService) Receive(request *http.Request) (any, int, error) {
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
	config := s.Store.Settings.Channel
	token := event.Header.Token
	if event.Type == "url_verification" {
		token = event.Token
	}
	if !hmac.Equal([]byte(token), []byte(configured(config.VerificationToken))) {
		return nil, 0, failure("UNAUTHENTICATED", "Invalid Feishu verification token", 401, false)
	}
	if event.Type == "url_verification" {
		return map[string]string{"challenge": event.Challenge}, 200, nil
	}
	if event.Header.AppID != configured(config.AppID) {
		return nil, 0, failure("UNAUTHENTICATED", "Feishu application differs from the binding", 401, false)
	}
	if event.Header.EventType != "im.message.receive_v1" || event.Event.Sender.Type != "user" ||
		event.Event.Sender.ID.OpenID != configured(config.UserOpenID) || event.Event.Message.Chat != "p2p" {
		return map[string]int{"code": 0}, 200, nil
	}
	message := feishuMessage{event.Event.Message.ID, event.Event.Message.Type, event.Event.Message.Content}
	if !feishuIDPattern.MatchString(message.ID) || !feishuIDPattern.MatchString(event.Header.EventID) {
		return nil, 0, failure("INVALID_ARGUMENT", "Invalid Feishu event or message identifier", 422, false)
	}
	payload, _ := json.Marshal(message)
	hash := hashBytes(payload)
	sealed, err := s.seal(message.ID, payload)
	if err != nil {
		return nil, 0, err
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
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
			configured(config.AppID), configured(config.UserOpenID), sealed, newID(), nowSeconds(), nowText())
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	s.Wake()
	return map[string]int{"code": 0}, 200, nil
}

func (s *FeishuService) Run(ctx context.Context) error {
	config := s.Store.Settings.Channel
	if _, err := s.Store.state.Exec(`UPDATE channel_events SET status = 'pending' WHERE status = 'processing'
		AND app_id = ? AND sender_open_id = ?`, configured(config.AppID), configured(config.UserOpenID)); err != nil {
		return err
	}
	rows, err := s.Store.state.Query(`SELECT message_id FROM channel_events WHERE status = 'processed'
		AND reply_status != 'queued' AND app_id = ? AND sender_open_id = ?`,
		configured(config.AppID), configured(config.UserOpenID))
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
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress, err := s.processOne(ctx)
		if err != nil {
			return err
		}
		if progress {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *FeishuService) processOne(ctx context.Context) (bool, error) {
	config := s.Store.Settings.Channel
	var messageID, status, kind, resource string
	var payload []byte
	var attempts int
	err := s.Store.state.QueryRow(`SELECT message_id, status, COALESCE(result_kind, ''),
		COALESCE(resource_name, ''), payload, attempts FROM channel_events
		WHERE (status = 'pending' OR status = 'processed' AND reply_status = 'pending') AND available_at <= ?
		AND app_id = ? AND sender_open_id = ? ORDER BY available_at, created_at LIMIT 1`,
		nowSeconds(), configured(config.AppID), configured(config.UserOpenID)).
		Scan(&messageID, &status, &kind, &resource, &payload, &attempts)
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
		body, err = s.unseal(messageID, payload)
		var message feishuMessage
		if err == nil {
			err = json.Unmarshal(body, &message)
		}
		if err == nil {
			kind, resource, err = s.execute(ctx, message)
		}
		if err != nil {
			var service *ServiceError
			if errors.As(err, &service) && !service.Retryable {
				kind, resource = "error", service.Code
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

const feishuHelp = "可用操作：\n记录 正文（可含 #标签）\n查看 memos/资源标识\n搜索 查询词（可加 #标签）\n相关 memos/资源标识\n回顾\n换一组\n打开 批次标识 memos/资源标识\n略过 批次标识 memos/资源标识\n发送图片或文件会保存为附件笔记。"

func (s *FeishuService) execute(ctx context.Context, message feishuMessage) (string, string, error) {
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
		note, _, err := s.Records.Create(ctx, key, CreateNote{Content: "附件：" + file.Filename, Attachments: []CreateAttachment{file}})
		return "saved", note.Name, err
	}
	if message.Type != "text" {
		return "help", "", nil
	}
	text := strings.TrimSpace(content.Text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "help", "", nil
	}
	switch fields[0] {
	case "相关":
		if len(fields) == 2 && memoPattern.MatchString(fields[1]) {
			return "related", fields[1], nil
		}
	case "搜索":
		request := SearchRequest{Query: strings.TrimSpace(strings.TrimPrefix(text, "搜索")), Limit: 5}
		words := []string{}
		for _, word := range strings.Fields(request.Query) {
			if strings.HasPrefix(word, "#") {
				request.Tags = append(request.Tags, strings.TrimPrefix(word, "#"))
			} else {
				words = append(words, word)
			}
		}
		if len(words) > 0 {
			request.Query = strings.Join(words, " ")
		}
		request, err := normalizeSearch(request)
		if err != nil {
			return "", "", err
		}
		body, _ := json.Marshal(request)
		sealed, err := s.seal("search:"+message.ID, body)
		return "search", message.ID + "." + base64.RawURLEncoding.EncodeToString(sealed), err
	case "记录":
		note, _, err := s.Records.Create(ctx, key, CreateNote{Content: strings.TrimSpace(strings.TrimPrefix(text, "记录"))})
		return "saved", note.Name, err
	case "查看":
		if len(fields) == 2 && memoPattern.MatchString(fields[1]) {
			return "memo", fields[1], nil
		}
	case "回顾":
		if len(fields) == 1 {
			batch, err := s.Reviews.Daily(ctx, time.Now())
			return "review", batch.ID, err
		}
	case "换一组":
		if len(fields) == 1 {
			batch, err := s.Reviews.Replace(ctx, key, time.Now())
			return "review", batch.ID, err
		}
	case "打开", "略过":
		if len(fields) == 3 {
			action := map[string]string{"打开": "open", "略过": "skip"}[fields[0]]
			batch, err := s.Reviews.Mark(ctx, fields[1], fields[2], action, time.Now())
			return "review", batch.ID, err
		}
	}
	return "help", "", nil
}

func excerpt(content string, limit int) string {
	runes := []rune(content)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return content
}

func (s *FeishuService) render(ctx context.Context, kind, resource string) (string, error) {
	switch kind {
	case "related":
		result, err := s.Search.Related(ctx, RelatedRequest{Name: resource, Limit: 5})
		if errorCode(err) == "SOURCE_NOT_FOUND" {
			return "笔记已删除或不可访问。", nil
		}
		if err != nil {
			return "", err
		}
		return "相关笔记\n原笔记：" + result.Source.Name + "\n" + result.Source.URL + "\n" +
			renderSearchResults(result.Results, result.Semantic), nil
	case "search":
		id, encoded, ok := strings.Cut(resource, ".")
		if !ok {
			return "", upstreamError("CHANNEL_QUEUE_KEY", "Invalid encrypted search request")
		}
		sealed, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return "", err
		}
		body, err := s.unseal("search:"+id, sealed)
		if err != nil {
			return "", err
		}
		var request SearchRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return "", err
		}
		result, err := s.Search.Search(ctx, request)
		if err != nil {
			return "", err
		}
		return "搜索结果\n" + renderSearchResults(result.Results, result.Semantic), nil
	case "help":
		return feishuHelp, nil
	case "error":
		return "操作失败：" + resource + "。请检查参数后重新发送。", nil
	case "saved", "memo":
		if err := s.Records.verify(ctx); err != nil {
			return "", err
		}
		memo, err := s.Source.Get(ctx, resource)
		if err != nil {
			return "", err
		}
		if memo == nil {
			return "笔记已删除或不可访问。", nil
		}
		if err := s.Records.verify(ctx); err != nil {
			return "", err
		}
		prefix := "已保存"
		if kind == "memo" {
			prefix = excerpt(memo.Content, 1500)
		}
		return prefix + "\n" + memo.Name + "\n" + memo.Note(s.Store.Settings).URL, nil
	case "review":
		batch, err := s.Reviews.Get(ctx, resource)
		if err != nil {
			return "", err
		}
		return renderReview(batch, true), nil
	default:
		return "", fmt.Errorf("invalid Feishu event result")
	}
}

func renderSearchResults(results []SearchHit, semantic SearchSemantic) string {
	var text strings.Builder
	if !semantic.Used {
		text.WriteString("仅使用关键词与标签（" + semantic.Reason + "）。\n")
	}
	if len(results) == 0 {
		text.WriteString("没有找到符合条件的笔记。")
	}
	for _, hit := range results {
		if len(hit.Matches) > 0 {
			match := hit.Matches[0]
			fmt.Fprintf(&text, "\n%s\n第 %d–%d 行\n%s\n%s\n", excerpt(match.Content, 300),
				match.LineStart, match.LineEnd, hit.Note.Name, hit.Note.URL)
		}
	}
	return text.String()
}

func renderReview(batch ReviewBatch, interactive bool) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s 每日回顾\n批次：%s\n", batch.Date, batch.ID)
	if len(batch.Items) == 0 {
		text.WriteString("没有符合规则的笔记。")
	}
	for _, item := range batch.Items {
		if item.Unavailable {
			continue
		}
		fmt.Fprintf(&text, "\n%s\n%s\n%s\n%s\n", item.Note.CreateTime, excerpt(item.Note.Content, 300), item.Name, item.Note.URL)
	}
	if interactive {
		text.WriteString("\n回复“换一组”，或使用“打开/略过 批次标识 memos/资源标识”。")
	}
	return text.String()
}
