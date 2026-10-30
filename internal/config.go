package moerise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
)

const Version = "0.2.0"

type MemosConfig struct {
	InstanceID     string  `json:"instance_id"`
	BaseURL        string  `json:"base_url"`
	PublicURL      string  `json:"public_url"`
	User           string  `json:"user"`
	Token          string  `json:"token"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
	PageSize       int     `json:"page_size"`
	MaxPages       int     `json:"max_pages"`
}

type WebConfig struct {
	Enabled            bool   `json:"enabled"`
	PublicOrigin       string `json:"public_origin"`
	StateEncryptionKey string `json:"state_encryption_key"`
}

type AccessToken struct {
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Scopes []string `json:"scopes"`
}

type SyncConfig struct {
	PollSeconds    int `json:"poll_seconds"`
	FullSeconds    int `json:"full_seconds"`
	OverlapSeconds int `json:"overlap_seconds"`
	RetrySeconds   int `json:"retry_seconds"`
	LeaseSeconds   int `json:"lease_seconds"`
}

type ReviewConfig struct {
	Count           int  `json:"count"`
	MinAgeHours     int  `json:"min_age_hours"`
	RecentDays      int  `json:"recent_days"`
	TopicLimit      int  `json:"topic_limit"`
	IncludeArchived bool `json:"include_archived"`
}

type NotificationConfig struct {
	Enabled bool     `json:"enabled"`
	Time    string   `json:"time"`
	Times   []string `json:"times,omitempty"`
}

type LLMConfig struct {
	Enabled         bool    `json:"enabled"`
	Protocol        string  `json:"protocol"`
	BaseURL         *string `json:"base_url"`
	APIKey          *string `json:"api_key"`
	Model           *string `json:"model"`
	TimeoutSeconds  float64 `json:"timeout_seconds"`
	MaxConcurrency  int     `json:"max_concurrency"`
	MaxRetries      int     `json:"max_retries"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	ContextBytes    int     `json:"context_bytes"`
}

type EmbeddingConfig struct {
	Enabled          bool    `json:"enabled"`
	Provider         string  `json:"provider"`
	Protocol         string  `json:"protocol"`
	BaseURL          *string `json:"base_url"`
	APIKey           *string `json:"api_key"`
	Model            *string `json:"model"`
	Dimensions       int     `json:"dimensions"`
	BatchSize        int     `json:"batch_size"`
	TimeoutSeconds   float64 `json:"timeout_seconds"`
	MaxConcurrency   int     `json:"max_concurrency"`
	MaxRetries       int     `json:"max_retries"`
	QueryInstruction *string `json:"query_instruction"`
}

type ChannelConfig struct {
	Kind              string  `json:"kind"`
	AppID             *string `json:"app_id"`
	AppSecret         *string `json:"app_secret"`
	VerificationToken *string `json:"verification_token"`
	EncryptKey        *string `json:"encrypt_key"`
	UserOpenID        *string `json:"user_open_id"`
	Destination       *string `json:"destination"`
	WebhookURL        *string `json:"webhook_url"`
	WebhookSecret     *string `json:"webhook_secret"`
}

func configured(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

type Settings struct {
	Memos         MemosConfig        `json:"memos"`
	Web           WebConfig          `json:"web"`
	AccessTokens  []AccessToken      `json:"access_tokens"`
	CursorSecret  string             `json:"cursor_secret"`
	Timezone      string             `json:"timezone"`
	StateDir      string             `json:"state_dir"`
	IndexDir      string             `json:"index_dir"`
	Sync          SyncConfig         `json:"sync"`
	Review        ReviewConfig       `json:"review"`
	Notifications NotificationConfig `json:"notifications"`
	Embedding     EmbeddingConfig    `json:"embedding"`
	LLM           LLMConfig          `json:"llm"`
	Channel       ChannelConfig      `json:"channel"`
	Insights      InsightsConfig     `json:"insights"`
}

func DefaultSettings() Settings {
	embeddingModel := "qwen3.7-text-embedding"
	return Settings{
		Memos:    MemosConfig{TimeoutSeconds: 10, PageSize: 100, MaxPages: 10000},
		Timezone: "Asia/Hong_Kong", StateDir: "/data/state", IndexDir: "/data/index",
		Sync:          SyncConfig{60, 21600, 300, 5, 300},
		Review:        ReviewConfig{Count: 3, MinAgeHours: 72, RecentDays: 7},
		Notifications: NotificationConfig{Time: "09:00"},
		Embedding: EmbeddingConfig{Provider: "bailian", Protocol: "dashscope", Model: &embeddingModel,
			Dimensions: 1024, BatchSize: 20, TimeoutSeconds: 30, MaxConcurrency: 2, MaxRetries: 2},
		LLM: LLMConfig{Protocol: "openai-compatible", TimeoutSeconds: 90, MaxConcurrency: 2,
			MaxRetries: 2, MaxOutputTokens: 2048, ContextBytes: 24000},
		Channel:  ChannelConfig{Kind: "feishu-app"},
		Insights: InsightsConfig{DefaultPrompt: DefaultInsightPrompt},
	}
}

func strictJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := rejectNull(raw, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON value")
	}
	return nil
}

func rejectNull(value any, kind reflect.Type) error {
	if value == nil {
		if kind.Kind() == reflect.Pointer {
			return nil
		}
		return errors.New("null is not allowed for this field")
	}
	if kind.Kind() == reflect.Struct {
		if object, ok := value.(map[string]any); ok {
			for i := range kind.NumField() {
				field := kind.Field(i)
				if child, exists := object[strings.Split(field.Tag.Get("json"), ",")[0]]; exists {
					if err := rejectNull(child, field.Type); err != nil {
						return err
					}
				}
			}
		}
	}
	if kind.Kind() == reflect.Slice {
		if kind.Elem().Kind() == reflect.Uint8 {
			if _, ok := value.(string); !ok {
				return errors.New("binary content must be a base64 string")
			}
		}
		if list, ok := value.([]any); ok {
			for _, child := range list {
				if err := rejectNull(child, kind.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func LoadSettings(path string) (Settings, error) {
	settings := DefaultSettings()
	content, err := os.ReadFile(path)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && os.Getenv("MOERISE_MEMOS_BASE_URL") != "") {
		return settings, errors.New("cannot read the configuration file")
	}
	if len(content) != 0 {
		if err := strictJSON(bytes.NewReader(content), &settings); err != nil {
			return settings, errors.New("invalid configuration structure")
		}
	}
	if err := settings.loadEnvironment(); err != nil {
		return settings, err
	}
	settings, err = loadPersistedProduct(settings)
	if err != nil {
		return settings, err
	}
	return settings, settings.Validate()
}

var instancePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)
var userPattern = regexp.MustCompile(`^users/[a-zA-Z0-9_-]+$`)
var memoPattern = regexp.MustCompile(`^memos/[a-zA-Z0-9_-]+$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validOrigin(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" &&
		!parsed.ForceQuery && parsed.Fragment == "" && strings.Trim(parsed.Path, "/") == ""
}

func (s *Settings) Validate() error {
	bad := func(field string) error { return fmt.Errorf("invalid configuration at %s", field) }
	if err := s.Web.validate(); err != nil {
		return err
	}
	if !instancePattern.MatchString(s.Memos.InstanceID) {
		return bad("memos.instance_id")
	}
	if !validOrigin(s.Memos.BaseURL) || !validOrigin(s.Memos.PublicURL) {
		return bad("memos URL")
	}
	s.Memos.BaseURL = strings.TrimRight(s.Memos.BaseURL, "/")
	s.Memos.PublicURL = strings.TrimRight(s.Memos.PublicURL, "/")
	if !userPattern.MatchString(s.Memos.User) || len(s.Memos.Token) < 16 {
		return bad("memos identity")
	}
	if s.Memos.TimeoutSeconds < 1 || s.Memos.TimeoutSeconds > 120 ||
		s.Memos.PageSize < 1 || s.Memos.PageSize > 1000 ||
		s.Memos.MaxPages < 1 || s.Memos.MaxPages > 100000 {
		return bad("memos limits")
	}
	if len(s.AccessTokens) == 0 || len(s.CursorSecret) < 32 {
		return bad("access credentials")
	}
	names, tokens := map[string]bool{}, map[string]bool{}
	for _, access := range s.AccessTokens {
		if access.Name == "" || len(access.Name) > 80 || len(access.Token) < 32 ||
			names[access.Name] || tokens[access.Token] || access.Token == s.Memos.Token ||
			len(access.Scopes) == 0 {
			return bad("access_tokens")
		}
		for _, scope := range access.Scopes {
			if !slices.Contains([]string{"notes:read", "notes:write", "sync:read", "sync:write", "reviews:read", "reviews:write", "discovery:write", "ai:generate"}, scope) {
				return bad("access_tokens.scopes")
			}
		}
		names[access.Name], tokens[access.Token] = true, true
	}
	if s.Web.Enabled && (s.Web.StateEncryptionKey == s.CursorSecret ||
		s.Web.StateEncryptionKey == s.Memos.Token || tokens[s.Web.StateEncryptionKey]) {
		return bad("web.state_encryption_key must be independent")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" {
		return bad("timezone")
	}
	state, err := filepath.Abs(s.StateDir)
	if err != nil || s.StateDir == "" {
		return bad("state_dir")
	}
	index, err := filepath.Abs(s.IndexDir)
	same := state == index || (runtime.GOOS == "windows" && strings.EqualFold(state, index))
	if stateInfo, statErr := os.Stat(state); statErr == nil {
		if indexInfo, statErr := os.Stat(index); statErr == nil && os.SameFile(stateInfo, indexInfo) {
			same = true
		}
	}
	if err != nil || s.IndexDir == "" || same {
		return bad("index_dir")
	}
	if s.Sync.PollSeconds < 1 || s.Sync.PollSeconds > 86400 ||
		s.Sync.FullSeconds < 5 || s.Sync.FullSeconds > 604800 ||
		s.Sync.OverlapSeconds < 1 || s.Sync.OverlapSeconds > 86400 ||
		s.Sync.RetrySeconds < 1 || s.Sync.RetrySeconds > 3600 ||
		s.Sync.LeaseSeconds < 10 || s.Sync.LeaseSeconds > 3600 {
		return bad("sync")
	}
	if s.Channel.Kind != "feishu-app" && s.Channel.Kind != "feishu-webhook" {
		return bad("channel.kind")
	}
	channel := s.Channel
	appFields := []string{configured(channel.AppID), configured(channel.AppSecret),
		configured(channel.VerificationToken), configured(channel.UserOpenID)}
	appEnabled := slices.ContainsFunc(appFields, func(value string) bool { return value != "" }) || configured(channel.EncryptKey) != ""
	if appEnabled && channel.Kind == "feishu-app" {
		if slices.Contains(appFields, "") ||
			len(configured(channel.AppSecret)) < 16 || len(configured(channel.VerificationToken)) < 16 ||
			!strings.HasPrefix(configured(channel.UserOpenID), "ou_") ||
			(configured(channel.Destination) != "" && configured(channel.Destination) != configured(channel.UserOpenID)) {
			return bad("channel application credentials or identity")
		}
	}
	if appEnabled && channel.Kind == "feishu-webhook" {
		if configured(channel.AppID) == "" || len(configured(channel.AppSecret)) < 16 ||
			configured(channel.VerificationToken) != "" || configured(channel.UserOpenID) != "" ||
			configured(channel.EncryptKey) != "" || configured(channel.Destination) != "" {
			return bad("channel Webhook image upload credentials")
		}
	}
	if s.Review.Count < 1 || s.Review.Count > 20 || s.Review.MinAgeHours < 0 ||
		s.Review.MinAgeHours > 87600 || s.Review.RecentDays < 0 || s.Review.RecentDays > 3650 {
		return bad("review")
	}
	if len(s.Notifications.Clocks()) > 24 {
		return bad("notifications.times")
	}
	clocks := map[string]bool{}
	for _, clock := range s.Notifications.Clocks() {
		if len(clock) != 5 {
			return bad("notifications.times")
		}
		if _, err := time.Parse("15:04", clock); err != nil || clocks[clock] {
			return bad("notifications.times")
		}
		clocks[clock] = true
	}
	if s.Notifications.Enabled && channel.Kind == "feishu-app" && !appEnabled {
		return bad("notifications require channel application credentials")
	}
	if s.Notifications.Enabled && channel.Kind == "feishu-webhook" && configured(channel.WebhookURL) == "" {
		return bad("notifications require channel.webhook_url")
	}
	if address := configured(channel.WebhookURL); address != "" {
		webhook, err := url.Parse(address)
		if err != nil || webhook.Host == "" || (webhook.Scheme != "http" && webhook.Scheme != "https") ||
			webhook.User != nil || webhook.Fragment != "" {
			return bad("channel.webhook_url")
		}
	}
	if err := s.Embedding.Validate(); err != nil {
		return err
	}
	return s.LLM.Validate()
}
