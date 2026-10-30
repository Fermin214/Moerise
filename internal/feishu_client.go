package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FeishuClient struct {
	config  ChannelConfig
	baseURL string
	http    *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewFeishuClient(config ChannelConfig) *FeishuClient {
	return &FeishuClient{config: config, baseURL: "https://open.feishu.cn/open-apis",
		http: &http.Client{Timeout: 15 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *FeishuClient) tenantToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": configured(c.config.AppID), "app_secret": configured(c.config.AppSecret)})
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid Feishu endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu authentication is unavailable")
	}
	defer response.Body.Close()
	var result struct {
		Code   int    `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil ||
		result.Code != 0 || result.Token == "" || result.Expire < 1 {
		return "", upstreamError("CHANNEL_AUTH", "Feishu rejected application credentials")
	}
	c.token, c.expires = result.Token, time.Now().Add(time.Duration(max(1, result.Expire-60))*time.Second)
	return c.token, nil
}

func (c *FeishuClient) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := c.tenantToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, upstreamError("CHANNEL_PROTOCOL", "Invalid Feishu request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if method == "POST" {
			return nil, upstreamError("SEND_OUTCOME_UNKNOWN", "Feishu send outcome is unknown")
		}
		return nil, upstreamError("CHANNEL_UNAVAILABLE", "Feishu is unavailable")
	}
	if response.StatusCode == 401 {
		c.mu.Lock()
		c.token, c.expires = "", time.Time{}
		c.mu.Unlock()
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		if method == "POST" && response.StatusCode >= 500 {
			return nil, upstreamError("SEND_OUTCOME_UNKNOWN", "Feishu send outcome is unknown")
		}
		return nil, upstreamError("CHANNEL_REJECTED", "Feishu rejected the request")
	}
	return response, nil
}

func (c *FeishuClient) Reply(ctx context.Context, messageID, uuid, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]any{"msg_type": "text", "content": string(content), "uuid": uuid})
	return c.send(ctx, "/im/v1/messages/"+url.PathEscape(messageID)+"/reply", body)
}

func (c *FeishuClient) SendText(ctx context.Context, recipient, uuid, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]string{"receive_id": recipient, "msg_type": "text",
		"content": string(content), "uuid": uuid})
	return c.send(ctx, "/im/v1/messages?receive_id_type=open_id", body)
}

func (c *FeishuClient) UploadImage(ctx context.Context, image CreateAttachment) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("image_type", "message"); err != nil {
		return "", err
	}
	file, err := writer.CreateFormFile("image", image.Filename)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(image.Content); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	token, err := c.tenantToken(ctx)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/im/v1/images", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		// Uploading a resource does not deliver a message and can be retried.
		return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu image upload must be retried")
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		c.mu.Lock()
		c.token, c.expires = "", time.Time{}
		c.mu.Unlock()
	}
	if response.StatusCode >= 500 || response.StatusCode == 429 {
		return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu image upload must be retried")
	}
	var result struct {
		Code *int `json:"code"`
		Data struct {
			ImageKey string `json:"image_key"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil || result.Code == nil {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid Feishu image upload response")
	}
	switch *result.Code {
	case 234006, 234010, 234011, 234039:
		return "", failure("CHANNEL_IMAGE_UNSUPPORTED", "Feishu cannot display this image", 422, false)
	}
	if response.StatusCode != 200 || *result.Code != 0 || result.Data.ImageKey == "" {
		return "", upstreamError("CHANNEL_REJECTED", "Feishu rejected the image upload")
	}
	return result.Data.ImageKey, nil
}

func (c *FeishuClient) SendImage(ctx context.Context, recipient, uuid, key string) (string, error) {
	content, _ := json.Marshal(map[string]string{"image_key": key})
	body, _ := json.Marshal(map[string]string{"receive_id": recipient, "msg_type": "image",
		"content": string(content), "uuid": uuid})
	return c.send(ctx, "/im/v1/messages?receive_id_type=open_id", body)
}

func (c *FeishuClient) send(ctx context.Context, path string, body []byte) (string, error) {
	response, err := c.request(ctx, "POST", path, body)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		Code *int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil || result.Code == nil {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Feishu returned an untrusted send response")
	}
	if *result.Code != 0 {
		return "", upstreamError("CHANNEL_REJECTED", "Feishu rejected the message")
	}
	if result.Data.MessageID == "" {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Feishu did not return a receipt")
	}
	return result.Data.MessageID, nil
}

type FeishuSentMessage struct {
	ID           string
	ParentID     string
	SenderID     string
	SenderIDType string
	SenderType   string
	Text         string
	Deleted      bool
	ImageKey     string
	ChatID       string
	CreateTime   time.Time
}

func (c *FeishuClient) GetMessage(ctx context.Context, id string) (FeishuSentMessage, error) {
	if !feishuIDPattern.MatchString(id) {
		return FeishuSentMessage{}, failure("INVALID_ARGUMENT", "Invalid Feishu message receipt", 422, false)
	}
	response, err := c.request(ctx, "GET", "/im/v1/messages/"+url.PathEscape(id), nil)
	if err != nil {
		return FeishuSentMessage{}, err
	}
	defer response.Body.Close()
	var result struct {
		Code *int `json:"code"`
		Data struct {
			Items []struct {
				ID       string `json:"message_id"`
				ParentID string `json:"parent_id"`
				Deleted  bool   `json:"deleted"`
				ChatID   string `json:"chat_id"`
				Created  string `json:"create_time"`
				Sender   struct {
					ID   string `json:"id"`
					Type string `json:"sender_type"`
					Kind string `json:"id_type"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result) != nil || result.Code == nil {
		return FeishuSentMessage{}, upstreamError("CHANNEL_PROTOCOL", "Invalid Feishu receipt response")
	}
	if *result.Code != 0 {
		return FeishuSentMessage{}, upstreamError("CHANNEL_REJECTED", "Feishu rejected receipt lookup")
	}
	for _, item := range result.Data.Items {
		if item.ID != id {
			continue
		}
		var content struct {
			Text     string `json:"text"`
			ImageKey string `json:"image_key"`
		}
		if json.Unmarshal([]byte(item.Body.Content), &content) != nil {
			return FeishuSentMessage{}, upstreamError("CHANNEL_PROTOCOL", "Invalid Feishu message content")
		}
		message := FeishuSentMessage{ID: item.ID, ParentID: item.ParentID, SenderID: item.Sender.ID,
			SenderIDType: item.Sender.Kind, SenderType: item.Sender.Type, Text: content.Text,
			Deleted: item.Deleted, ImageKey: content.ImageKey, ChatID: item.ChatID}
		if millis, err := strconv.ParseInt(item.Created, 10, 64); err == nil {
			message.CreateTime = time.UnixMilli(millis)
		}
		return message, nil
	}
	return FeishuSentMessage{}, failure("NOTIFICATION_CONFLICT", "Feishu receipt was not found", 409, false)
}

func (c *FeishuClient) Download(ctx context.Context, messageID, key, kind, filename string) (CreateAttachment, error) {
	query := url.Values{"type": {kind}}
	response, err := c.request(ctx, "GET", "/im/v1/messages/"+url.PathEscape(messageID)+"/resources/"+url.PathEscape(key)+"?"+query.Encode(), nil)
	if err != nil {
		return CreateAttachment{}, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return CreateAttachment{}, upstreamError("CHANNEL_UNAVAILABLE", "Feishu file download failed")
	}
	if len(content) == 0 || len(content) > 8*1024*1024 {
		return CreateAttachment{}, failure("INVALID_ARGUMENT", "Feishu attachment must contain 1 byte to 8 MiB", 422, false)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(content))
	}
	if kind == "image" {
		extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[mediaType]
		if extension == "" {
			extension = ".bin"
		}
		filename = "image" + extension
	}
	if filename == "" {
		filename = "attachment.bin"
	}
	return CreateAttachment{Filename: filename, Type: strings.ToLower(mediaType), Content: content}, nil
}
