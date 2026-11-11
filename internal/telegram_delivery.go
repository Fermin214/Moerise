package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// TelegramDelivery sends scheduled reviews through the Telegram Bot API.
type TelegramDelivery struct {
	Store   *Store
	Reviews *ReviewService
	config  ChannelConfig
	http    *http.Client
	baseURL string
}

func newTelegramDelivery(config ChannelConfig, store *Store, reviews *ReviewService) NotificationTransport {
	if configured(config.BotToken) == "" || configured(config.Destination) == "" {
		return nil
	}
	return &TelegramDelivery{Store: store, Reviews: reviews, config: config, baseURL: "https://api.telegram.org",
		http: &http.Client{Timeout: 15 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *TelegramDelivery) Send(ctx context.Context, item Notification) (string, error) {
	if item.Kind != "review" || item.BatchID == nil {
		return "", failure("INVALID_ARGUMENT", "Telegram only supports review notifications", 422, false)
	}
	return s.reviewDelivery().sendReviewPart(ctx, item)
}

func (s *TelegramDelivery) Check(context.Context, Notification, string) (bool, error) {
	return false, failure("CHANNEL_RECEIPT_UNSUPPORTED", "Telegram outcomes require operator confirmation without a receipt", 422, false)
}

func (s *TelegramDelivery) reviewDelivery() *reviewDelivery {
	return &reviewDelivery{Store: s.Store, Reviews: s.Reviews, platform: s}
}

func (s *TelegramDelivery) reviewTitle() string      { return "Daily review" }
func (s *TelegramDelivery) reviewDatePrefix() string { return "Created on " }
func (s *TelegramDelivery) reviewImages() bool       { return true }

// Count UTF-16 units so astral characters also fit Telegram's text limit.
func (s *TelegramDelivery) reviewTextEnd(content string) (int, error) {
	end, units := len(content), 0
	for offset, r := range content {
		units += utf16.RuneLen(r)
		if units > 4096 {
			end = offset
			break
		}
	}
	if end < len(content) {
		if newline := strings.LastIndexByte(content[:end], '\n'); newline >= end/2 {
			end = newline + 1
		}
	}
	return end, nil
}

type telegramMessage struct {
	ID   int64 `json:"message_id"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	Document struct {
		FileID string `json:"file_id"`
	} `json:"document"`
}

func (s *TelegramDelivery) request(ctx context.Context, method, contentType string, body io.Reader) (telegramMessage, error) {
	unknown := upstreamError("SEND_OUTCOME_UNKNOWN", "Telegram send outcome is unknown")
	var result struct {
		OK         *bool           `json:"ok"`
		Result     telegramMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/bot"+configured(s.config.BotToken)+"/"+method, body)
	if err != nil {
		return result.Result, failure("CHANNEL_PROTOCOL", "Invalid Telegram request", 422, false)
	}
	request.Header.Set("Content-Type", contentType)
	response, err := s.http.Do(request)
	if err != nil {
		// HTTP errors include the credential-bearing URL; never propagate them.
		return result.Result, unknown
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return result.Result, unknown
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	valid := err == nil && len(data) <= 1<<20 && json.Unmarshal(data, &result) == nil && result.OK != nil
	if valid && !*result.OK && result.ErrorCode >= 400 && result.ErrorCode < 500 {
		rejected := upstreamError("CHANNEL_REJECTED", "Telegram rejected the notification; check the bot token, chat ID and bot permissions")
		if result.ErrorCode == 429 {
			after := result.Parameters.RetryAfter
			if after <= 0 || after > 1<<31-1 {
				after = 60
			}
			return result.Result, &notificationRetryError{
				error: upstreamError("CHANNEL_RATE_LIMITED", "Telegram rate limit reached"), after: time.Duration(after) * time.Second}
		}
		return result.Result, rejected
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 429 {
		return result.Result, upstreamError("CHANNEL_REJECTED", "Telegram rejected the notification")
	}
	chat, _ := strconv.ParseInt(configured(s.config.Destination), 10, 64)
	bot, _, _ := strings.Cut(configured(s.config.BotToken), ":")
	botID, _ := strconv.ParseInt(bot, 10, 64)
	if !valid || !*result.OK || response.StatusCode != http.StatusOK || result.Result.ID <= 0 ||
		result.Result.Chat.ID != chat || !result.Result.From.IsBot || result.Result.From.ID != botID ||
		method == "sendDocument" && result.Result.Document.FileID == "" {
		return result.Result, unknown
	}
	return result.Result, nil
}

func (s *TelegramDelivery) receipt(message telegramMessage) string {
	return "telegram:" + configured(s.config.Destination) + ":" + strconv.FormatInt(message.ID, 10)
}

func (s *TelegramDelivery) sendReviewText(ctx context.Context, _ Notification, text string) (string, error) {
	body, _ := json.Marshal(map[string]any{"chat_id": configured(s.config.Destination), "text": text,
		"link_preview_options": map[string]bool{"is_disabled": true}})
	message, err := s.request(ctx, "sendMessage", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	return s.receipt(message), nil
}

func (s *TelegramDelivery) downloadImage(ctx context.Context, part reviewDeliveryPart, note Note) (CreateAttachment, error) {
	for _, attachment := range note.Attachments {
		if attachment["name"] != part.Attachment {
			continue
		}
		source, ok := s.Reviews.Source.(interface {
			DownloadAttachment(context.Context, map[string]any) (CreateAttachment, error)
		})
		if !ok {
			return CreateAttachment{}, upstreamError("UPSTREAM_PROTOCOL", "Source cannot download images")
		}
		image, err := source.DownloadAttachment(ctx, attachment)
		if err != nil {
			return CreateAttachment{}, err
		}
		if len(image.Content) == 0 || len(image.Content) > 10*1024*1024 {
			return CreateAttachment{}, failure("CHANNEL_IMAGE_SIZE", "Image must contain 1 byte to 10 MiB", 422, false)
		}
		return image, nil
	}
	return CreateAttachment{}, failure("SOURCE_NOT_FOUND", "Review image is unavailable", 404, false)
}

func (s *TelegramDelivery) reviewImageKey(ctx context.Context, _ string, part reviewDeliveryPart, note Note) (string, error) {
	if part.ImageKey != "" {
		return part.ImageKey, nil
	}
	// Telegram uploads send immediately. Check availability before the title without sending.
	_, err := s.downloadImage(ctx, part, note)
	if err != nil {
		return "", err
	}
	return part.Attachment, nil
}

func (s *TelegramDelivery) sendReviewImage(ctx context.Context, item Notification, part reviewDeliveryPart, note Note) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", configured(s.config.Destination)); err != nil {
		return "", err
	}
	if part.ImageKey != "" {
		if err := writer.WriteField("document", part.ImageKey); err != nil {
			return "", err
		}
	} else {
		image, err := s.downloadImage(ctx, part, note)
		if err != nil {
			return "", err
		}
		file, err := writer.CreateFormFile("document", image.Filename)
		if err != nil {
			return "", err
		}
		if _, err := file.Write(image.Content); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	message, err := s.request(ctx, "sendDocument", writer.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	if _, err := s.Store.state.Exec(`UPDATE feishu_review_parts SET image_key = ? WHERE notification_id = ?`,
		message.Document.FileID, item.ID); err != nil {
		return "", err
	}
	return s.receipt(message), nil
}
