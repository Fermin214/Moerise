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

type ProviderConfig struct {
	Protocol string  `json:"protocol"`
	BaseURL  *string `json:"base_url"`
	APIKey   *string `json:"api_key"`
	Model    *string `json:"model"`
}

type ChannelConfig struct {
	Kind        string  `json:"kind"`
	AppID       *string `json:"app_id"`
	AppSecret   *string `json:"app_secret"`
	Destination *string `json:"destination"`
	WebhookURL  *string `json:"webhook_url"`
}

type Settings struct {
	Memos        MemosConfig    `json:"memos"`
	AccessTokens []AccessToken  `json:"access_tokens"`
	CursorSecret string         `json:"cursor_secret"`
	Timezone     string         `json:"timezone"`
	StateDir     string         `json:"state_dir"`
	IndexDir     string         `json:"index_dir"`
	Sync         SyncConfig     `json:"sync"`
	Embedding    ProviderConfig `json:"embedding"`
	LLM          ProviderConfig `json:"llm"`
	Channel      ChannelConfig  `json:"channel"`
}

func DefaultSettings() Settings {
	return Settings{
		Memos:    MemosConfig{TimeoutSeconds: 10, PageSize: 100, MaxPages: 10000},
		Timezone: "Asia/Hong_Kong", StateDir: "/data/state", IndexDir: "/data/index",
		Sync:      SyncConfig{60, 21600, 300, 5, 300},
		Embedding: ProviderConfig{Protocol: "openai-compatible"},
		LLM:       ProviderConfig{Protocol: "openai-compatible"},
		Channel:   ChannelConfig{Kind: "feishu-app"},
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
	if err != nil {
		return settings, errors.New("cannot read the configuration file")
	}
	if err := strictJSON(bytes.NewReader(content), &settings); err != nil {
		return settings, errors.New("invalid configuration structure")
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
			if !slices.Contains([]string{"notes:read", "notes:write", "sync:read", "sync:write"}, scope) {
				return bad("access_tokens.scopes")
			}
		}
		names[access.Name], tokens[access.Token] = true, true
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
	return nil
}
