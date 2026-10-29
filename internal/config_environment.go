package moerise

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

func (c NotificationConfig) Clocks() []string {
	if len(c.Times) == 0 {
		return []string{c.Time}
	}
	clocks := slices.Clone(c.Times)
	slices.Sort(clocks)
	return clocks
}

func (s *Settings) loadEnvironment() error {
	fields := []struct {
		name   string
		target any
	}{
		{"MEMOS_INSTANCE_ID", &s.Memos.InstanceID},
		{"MEMOS_BASE_URL", &s.Memos.BaseURL},
		{"MEMOS_PUBLIC_URL", &s.Memos.PublicURL},
		{"MEMOS_USER", &s.Memos.User},
		{"MEMOS_TOKEN", &s.Memos.Token},
		{"WEB_ENABLED", &s.Web.Enabled},
		{"PUBLIC_ORIGIN", &s.Web.PublicOrigin},
		{"STATE_ENCRYPTION_KEY", &s.Web.StateEncryptionKey},
		{"CURSOR_SECRET", &s.CursorSecret},
		{"TIMEZONE", &s.Timezone},
		{"STATE_DIR", &s.StateDir},
		{"INDEX_DIR", &s.IndexDir},
		{"REVIEW_COUNT", &s.Review.Count},
		{"REVIEW_MIN_AGE_HOURS", &s.Review.MinAgeHours},
		{"REVIEW_RECENT_DAYS", &s.Review.RecentDays},
		{"REVIEW_INCLUDE_ARCHIVED", &s.Review.IncludeArchived},
		{"NOTIFICATIONS_ENABLED", &s.Notifications.Enabled},
		{"NOTIFICATIONS_TIME", &s.Notifications.Time},
		{"NOTIFICATIONS_TIMES", &s.Notifications.Times},
		{"CHANNEL_KIND", &s.Channel.Kind},
		{"FEISHU_APP_ID", &s.Channel.AppID},
		{"FEISHU_APP_SECRET", &s.Channel.AppSecret},
		{"FEISHU_VERIFICATION_TOKEN", &s.Channel.VerificationToken},
		{"FEISHU_ENCRYPT_KEY", &s.Channel.EncryptKey},
		{"FEISHU_USER_OPEN_ID", &s.Channel.UserOpenID},
		{"FEISHU_WEBHOOK_URL", &s.Channel.WebhookURL},
		{"FEISHU_WEBHOOK_SECRET", &s.Channel.WebhookSecret},
	}
	for _, field := range fields {
		name := "MOERISE_" + field.name
		value, exists := os.LookupEnv(name)
		if !exists || value == "" {
			continue
		}
		var err error
		switch target := field.target.(type) {
		case *string:
			*target = value
		case **string:
			*target = &value
		case *bool:
			*target, err = strconv.ParseBool(value)
		case *int:
			*target, err = strconv.Atoi(value)
		case *[]string:
			*target = strings.Split(value, ",")
			for i := range *target {
				(*target)[i] = strings.TrimSpace((*target)[i])
			}
		}
		if err != nil {
			return fmt.Errorf("invalid environment variable %s", name)
		}
	}
	if token := os.Getenv("MOERISE_ACCESS_TOKEN"); token != "" {
		scopes := []string{"notes:read", "notes:write", "sync:read", "sync:write",
			"reviews:read", "reviews:write", "discovery:write", "ai:generate"}
		s.AccessTokens = []AccessToken{{Name: "environment", Token: token, Scopes: scopes}}
	}
	return nil
}
