package moerise

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The encoded request includes nested application JSON and Webhook signing fields.
const feishuReviewBytes = 18 * 1024

func (s *FeishuDelivery) reviewDelivery() *reviewDelivery {
	return &reviewDelivery{Store: s.Store, Reviews: s.Reviews, platform: s}
}

func (s *FeishuDelivery) reviewTextEnd(content string) (int, error) { return reviewTextEnd(content) }
func (s *FeishuDelivery) reviewTitle() string                       { return "「今日回顾」" }
func (s *FeishuDelivery) reviewDatePrefix() string                  { return "创建于" }
func (s *FeishuDelivery) reviewImages() bool {
	return configured(s.Store.Settings.Channel.AppID) != "" && configured(s.Store.Settings.Channel.AppSecret) != ""
}

func reviewTextFits(text string) bool {
	content, _ := json.Marshal(map[string]string{"text": text})
	// Double encoding for the application API is larger than the Webhook form.
	body, _ := json.Marshal(map[string]string{"receive_id": strings.Repeat("x", 128),
		"msg_type": "text", "uuid": strings.Repeat("x", 50), "content": string(content)})
	return len(body) <= feishuReviewBytes
}

func reviewTextEnd(content string) (int, error) {
	if len(content) <= feishuReviewBytes && reviewTextFits(content) {
		return len(content), nil
	}
	low, high := 0, min(len(content), feishuReviewBytes)
	for low < high {
		mid := (low + high + 1) / 2
		end := mid
		for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if reviewTextFits(content[:end]) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	end := low
	for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	if newline := strings.LastIndexByte(content[:end], '\n'); newline >= end/2 {
		end = newline + 1
	}
	if end == 0 {
		return 0, failure("CHANNEL_MESSAGE_SIZE", "Review text cannot fit in a message", 422, false)
	}
	return end, nil
}

func (s *FeishuDelivery) sendReviewText(ctx context.Context, item Notification, text string) (string, error) {
	if s.Store.Settings.Channel.Kind == "feishu-webhook" {
		return s.webhook(ctx, item.ID, text)
	}
	if s.Events == nil {
		return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu application is unavailable")
	}
	return s.Events.Client.SendText(ctx, configured(s.Store.Settings.Channel.UserOpenID), item.PlatformUUID, text)
}

type feishuImagePlatform interface {
	UploadImage(context.Context, CreateAttachment) (string, error)
	SendImage(context.Context, string, string, string) (string, error)
}

func (s *FeishuDelivery) imageClient() feishuImagePlatform {
	if s.Events != nil {
		if client, ok := s.Events.Client.(feishuImagePlatform); ok {
			return client
		}
	}
	return s.images
}

func (s *FeishuDelivery) sendReviewImage(ctx context.Context, item Notification, part reviewDeliveryPart, note Note) (string, error) {
	key, err := s.reviewImageKey(ctx, item.ID, part, note)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "empty:" + item.ID, nil
	}
	if s.Store.Settings.Channel.Kind == "feishu-webhook" {
		return s.webhookMessage(ctx, item.ID, "image", map[string]string{"image_key": key})
	}
	return s.imageClient().SendImage(ctx, configured(s.Store.Settings.Channel.UserOpenID), item.PlatformUUID, key)
}

func (s *FeishuDelivery) reviewImageKey(ctx context.Context, id string, part reviewDeliveryPart, note Note) (string, error) {
	if configured(s.Store.Settings.Channel.AppID) == "" {
		return "", nil
	}
	client := s.imageClient()
	key := part.ImageKey
	if key == "" {
		var attachment map[string]any
		for _, candidate := range note.Attachments {
			if candidate["name"] == part.Attachment {
				attachment = candidate
				break
			}
		}
		if attachment == nil {
			return "", failure("SOURCE_NOT_FOUND", "Review image is unavailable", 404, false)
		}
		source, ok := s.Reviews.Source.(interface {
			DownloadAttachment(context.Context, map[string]any) (CreateAttachment, error)
		})
		if !ok {
			return "", upstreamError("UPSTREAM_PROTOCOL", "Source cannot download images")
		}
		image, err := source.DownloadAttachment(ctx, attachment)
		if err != nil {
			return "", err
		}
		key, err = client.UploadImage(ctx, image)
		if err != nil {
			return "", err
		}
		if _, err := s.Store.state.Exec(`UPDATE review_delivery_parts SET image_key = ? WHERE notification_id = ?`,
			key, id); err != nil {
			return "", err
		}
	}
	return key, nil
}
