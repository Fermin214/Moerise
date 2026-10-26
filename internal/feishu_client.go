package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
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
		http: &http.Client{Timeout: 15 * time.Second,
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
	response, err := c.request(ctx, "POST", "/im/v1/messages/"+url.PathEscape(messageID)+"/reply", body)
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
