package moerise

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ProductSettings struct {
	Timezone      string             `json:"timezone"`
	Review        ReviewConfig       `json:"review"`
	Notifications NotificationConfig `json:"notifications"`
	Embedding     EmbeddingConfig    `json:"embedding"`
	Stargazing    StargazingConfig   `json:"stargazing"`
	Channel       ChannelConfig      `json:"channel"`

	Daily                *dailySettings             `json:"daily,omitempty"`
	NotificationChannels map[string]ChannelConfig   `json:"notification_channels,omitempty"`
	extras               map[string]json.RawMessage `json:"-"`
}

type dailySettings struct {
	Timezone      string             `json:"timezone"`
	Review        ReviewConfig       `json:"review"`
	Notifications NotificationConfig `json:"notifications"`
	Until         time.Time          `json:"until"`
	Version       int64              `json:"version,omitempty"`
}

type SecretUpdate struct {
	Action string  `json:"action"`
	Value  *string `json:"value,omitempty"`
}

type SettingsUpdate struct {
	Version  int64                      `json:"version"`
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	Secrets  map[string]SecretUpdate    `json:"secrets,omitempty"`
}

type SecretStatus struct {
	Configured bool `json:"configured"`
}

type ChannelCapabilities struct {
	Text   bool `json:"text"`
	Images bool `json:"images"`
	Record bool `json:"record"`
}

type SettingsView struct {
	Version             int64                   `json:"version"`
	Settings            map[string]any          `json:"settings"`
	Secrets             map[string]SecretStatus `json:"secrets"`
	ChannelCapabilities ChannelCapabilities     `json:"channel_capabilities"`
	ChannelStatus       map[string]botStatus    `json:"channel_status"`
}

func productSettings(s Settings) ProductSettings {
	return ProductSettings{s.Timezone, s.Review, s.Notifications, s.Embedding, s.Stargazing, s.Channel, s.daily, s.notificationChannels, cloneRawMap(s.productExtras)}
}

func (p ProductSettings) apply(s Settings) Settings {
	s.Timezone, s.Review, s.Notifications = p.Timezone, p.Review, p.Notifications
	s.Embedding, s.Channel = p.Embedding, p.Channel
	s.Stargazing = p.Stargazing
	s.daily = p.Daily
	s.notificationChannels = p.NotificationChannels
	s.productExtras = cloneRawMap(p.extras)
	return s
}

var productExtraSectionKeys = map[string]bool{
	"review": true, "notifications": true, "embedding": true,
	"stargazing": true, "channel": true, "daily": true,
}

func cloneRawMap(source map[string]json.RawMessage) map[string]json.RawMessage {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]json.RawMessage, len(source))
	for key, raw := range source {
		cloned[key] = bytes.Clone(raw)
	}
	return cloned
}

func extractProductExtras(plain []byte, product ProductSettings) map[string]json.RawMessage {
	var rawMap map[string]json.RawMessage
	if json.Unmarshal(plain, &rawMap) != nil || len(rawMap) == 0 {
		return nil
	}
	knownBytes, err := json.Marshal(product)
	if err != nil {
		return nil
	}
	var knownMap map[string]json.RawMessage
	if json.Unmarshal(knownBytes, &knownMap) != nil {
		return nil
	}
	extras := map[string]json.RawMessage{}
	for key, rawValue := range rawMap {
		knownValue, exists := knownMap[key]
		if !exists {
			extras[key] = bytes.Clone(rawValue)
			continue
		}
		if productExtraSectionKeys[key] {
			if child := subtractObjectExtras(rawValue, knownValue); child != nil {
				extras[key] = child
			}
		}
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

func subtractObjectExtras(rawValue, knownValue json.RawMessage) json.RawMessage {
	var rawObj, knownObj map[string]json.RawMessage
	if json.Unmarshal(rawValue, &rawObj) != nil || json.Unmarshal(knownValue, &knownObj) != nil || rawObj == nil || knownObj == nil {
		return nil
	}
	diff := map[string]json.RawMessage{}
	for key, rawChild := range rawObj {
		knownChild, exists := knownObj[key]
		if !exists {
			diff[key] = bytes.Clone(rawChild)
			continue
		}
		if nested := subtractObjectExtras(rawChild, knownChild); nested != nil {
			diff[key] = nested
		}
	}
	if len(diff) == 0 {
		return nil
	}
	encoded, err := json.Marshal(diff)
	if err != nil {
		return nil
	}
	return encoded
}

func mergeProductExtras(body []byte, extras map[string]json.RawMessage) ([]byte, error) {
	if len(extras) == 0 {
		return body, nil
	}
	var target map[string]json.RawMessage
	if err := json.Unmarshal(body, &target); err != nil {
		return nil, err
	}
	for key, rawExtra := range extras {
		baseValue, exists := target[key]
		if !exists {
			target[key] = bytes.Clone(rawExtra)
			continue
		}
		if productExtraSectionKeys[key] {
			if merged := mergeObjectExtras(baseValue, rawExtra); merged != nil {
				target[key] = merged
			}
		}
	}
	return json.Marshal(target)
}

func mergeObjectExtras(baseValue, extraValue json.RawMessage) json.RawMessage {
	var baseObj, extraObj map[string]json.RawMessage
	if json.Unmarshal(baseValue, &baseObj) != nil || json.Unmarshal(extraValue, &extraObj) != nil || baseObj == nil || extraObj == nil {
		return nil
	}
	for key, rawExtra := range extraObj {
		baseChild, exists := baseObj[key]
		if !exists {
			baseObj[key] = bytes.Clone(rawExtra)
			continue
		}
		if merged := mergeObjectExtras(baseChild, rawExtra); merged != nil {
			baseObj[key] = merged
		}
	}
	encoded, err := json.Marshal(baseObj)
	if err != nil {
		return nil
	}
	return encoded
}

func newStateCipher(settings Settings, purpose string) (cipher.AEAD, error) {
	key, err := stateEncryptionKey(settings.Web.StateEncryptionKey)
	if err != nil {
		return nil, err
	}
	key, err = hkdf.Key(sha256.New, key, nil, "moerise/state/"+purpose+"/v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func settingsAAD(settings Settings, version int64) []byte {
	return []byte(settings.Memos.InstanceID + "\x00" + settings.Memos.User + "\x00" + strconv.FormatInt(version, 10))
}

func encryptProduct(settings Settings, version int64) ([]byte, error) {
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(productSettings(settings))
	if err != nil {
		return nil, err
	}
	body, err = mergeProductExtras(body, settings.productExtras)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, body, settingsAAD(settings, version)), nil
}

func decryptProduct(settings Settings, version int64, body []byte) (Settings, error) {
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		return settings, err
	}
	bad := errors.New("cannot decrypt product settings with the configured state encryption key")
	if version < 1 || len(body) < aead.NonceSize()+aead.Overhead() {
		return settings, bad
	}
	plain, err := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], settingsAAD(settings, version))
	if err != nil {
		return settings, bad
	}
	product := ProductSettings{Stargazing: StargazingConfig{Enabled: true}}
	if tolerantJSON(bytes.NewReader(plain), &product) != nil {
		return settings, bad
	}
	product.normalizeDeliveryChannels()
	product.extras = extractProductExtras(plain, product)
	return product.apply(settings), nil
}

func storedProduct(db *sql.DB, settings Settings) (Settings, int64, error) {
	var version int64
	var body []byte
	err := db.QueryRow(`SELECT version, ciphertext FROM product_settings WHERE instance_id = ?`,
		settings.Memos.InstanceID).Scan(&version, &body)
	if errors.Is(err, sql.ErrNoRows) {
		saved, err := deploymentProduct(db, settings)
		return saved, 0, err
	}
	if err != nil {
		return settings, 0, err
	}
	settings, err = decryptProduct(settings, version, body)
	return settings, version, err
}

func deploymentProduct(db *sql.DB, settings Settings) (Settings, error) {
	if !settings.stargazingExplicit && !settings.Stargazing.Enabled {
		legacy, err := legacyDeployment(db, "legacy_stargazing_deployments", settings.Memos.InstanceID)
		if err != nil {
			return settings, err
		}
		settings.Stargazing.Enabled = legacy
	}
	if settings.Channel.FeishuMode != "" {
		return settings, nil
	}
	legacy, err := legacyDeployment(db, "legacy_channel_deployments", settings.Memos.InstanceID)
	if err != nil {
		return settings, err
	}
	if legacy {
		product := productSettings(settings)
		product.normalizeDeliveryChannels()
		return product.apply(settings), nil
	}
	settings.Channel.FeishuMode = "websocket"
	return settings, nil
}

func legacyDeployment(db *sql.DB, marker, instance string) (bool, error) {
	var migrated, bound, legacy bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?),
		EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='bindings')`, marker).Scan(&migrated, &bound); err != nil {
		return false, err
	}
	if bound {
		query := `SELECT EXISTS(SELECT 1 FROM bindings WHERE instance_id=?)`
		if migrated {
			query = `SELECT EXISTS(SELECT 1 FROM ` + marker + ` WHERE instance_id=?)`
		}
		if err := db.QueryRow(query, instance).Scan(&legacy); err != nil {
			return false, err
		}
	}
	return legacy, nil
}

// 部署值只初始化一次，后续读取持久版本。
func loadPersistedProduct(settings Settings) (Settings, error) {
	path := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return settings, nil
	} else if err != nil {
		return settings, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return settings, err
	}
	slash := filepath.ToSlash(absolute)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	address := url.URL{Scheme: "file", Path: slash, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return settings, err
	}
	defer db.Close()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'product_settings')`).Scan(&exists); err != nil {
		return settings, err
	}
	if !exists {
		return deploymentProduct(db, settings)
	}
	saved, _, err := storedProduct(db, settings)
	return saved, err
}

func (s *Store) initializeProductSettings() error {
	saved, version, err := storedProduct(s.state, s.Settings)
	if err != nil {
		return err
	}
	s.Settings, s.productVersion = saved, version
	if version > 0 {
		return nil
	}
	if s.Settings.Web.StateEncryptionKey == "" {
		return nil
	}
	body, err := encryptProduct(s.Settings, 1)
	if err != nil {
		return err
	}
	_, err = s.state.Exec(`INSERT INTO product_settings VALUES (?, 1, ?, ?)`,
		s.Settings.Memos.InstanceID, body, nowText())
	if err == nil {
		s.productVersion = 1
	}
	return err
}

func secretFields(product *ProductSettings) map[string]**string {
	return map[string]**string{
		"embedding_api_key": &product.Embedding.APIKey,
		"app_secret":        &product.Channel.AppSecret, "verification_token": &product.Channel.VerificationToken,
		"encrypt_key": &product.Channel.EncryptKey, "webhook_url": &product.Channel.WebhookURL,
		"webhook_secret": &product.Channel.WebhookSecret,
		"bot_token":      &product.Channel.BotToken,
		"qq_app_secret":  &product.Channel.QQAppSecret, "webhook_app_secret": &product.Channel.WebhookAppSecret,
	}
}

func publicProduct(product ProductSettings) map[string]any {
	product.Channel.FeishuMode = product.Channel.feishuMode()
	data, _ := json.Marshal(product)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	delete(result, "daily")
	delete(result, "notification_channels")
	notifications := result["notifications"].(map[string]any)
	clocks := product.Notifications.Clocks()
	notifications["times"] = clocks
	delete(result["embedding"].(map[string]any), "api_key")
	channel := result["channel"].(map[string]any)
	for _, key := range []string{"app_secret", "verification_token", "encrypt_key", "webhook_url", "webhook_secret", "bot_token", "qq_app_secret", "webhook_app_secret"} {
		delete(channel, key)
	}
	return result
}

func (s *Store) reviewSettings(at time.Time) Settings {
	settings := s.Settings
	if previous := settings.daily; previous != nil && at.Before(previous.Until) {
		settings.Timezone, settings.Review = previous.Timezone, previous.Review
		enabled := settings.Notifications.Enabled
		settings.Notifications = previous.Notifications
		settings.Notifications.Enabled = enabled
	} else {
		settings.daily = nil
	}
	return settings
}

func (s *Store) settingsView() SettingsView {
	product := productSettings(s.Settings)
	secrets := map[string]SecretStatus{}
	for key, field := range secretFields(&product) {
		secrets[key] = SecretStatus{configured(*field) != ""}
	}
	channel := s.deliveryChannel()
	record := product.Channel.botEnabled("feishu-app") || product.Channel.botEnabled("qq") ||
		product.Channel.botEnabled("telegram") && s.Settings.Web.StateEncryptionKey != ""
	text := channel.botEnabled(channel.Kind) && configured(channel.Destination) != "" ||
		channel.Kind == "feishu-webhook" && configured(channel.WebhookURL) != ""
	images := text && (channel.Kind == "feishu-app" || channel.Kind == "telegram" || channel.Kind == "feishu-webhook" && product.Channel.WebhookImages)
	return SettingsView{s.productVersion, publicProduct(product), secrets,
		ChannelCapabilities{text, images, record}, s.botStatuses()}
}

func mergeSettings(target map[string]any, changes map[string]json.RawMessage) error {
	bad := failure("INVALID_ARGUMENT", "Invalid settings fields", 422, false)
	for key, raw := range changes {
		previous, exists := target[key]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return bad
		}
		if object, ok := previous.(map[string]any); ok {
			var patch map[string]json.RawMessage
			if json.Unmarshal(raw, &patch) != nil || patch == nil {
				return bad
			}
			if err := mergeSettings(object, patch); err != nil {
				return err
			}
		} else {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return bad
			}
			target[key] = value
		}
	}
	return nil
}

func (s *Store) prepareSettings(update SettingsUpdate) (Settings, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid product settings", 422, false)
	if update.Version != s.productVersion {
		return s.Settings, failure("SETTINGS_CONFLICT", "Settings have changed; reload before saving", 409, false)
	}
	if s.productVersion == 0 {
		return s.Settings, failure("SETTINGS_UNAVAILABLE", "Configure the state encryption key before saving settings", 503, false)
	}
	product := productSettings(s.Settings)
	// 解码会复用非空指针，先复制配置，避免修改旧版本或使资源变化比较失真。
	original, err := json.Marshal(product)
	if err != nil {
		return s.Settings, err
	}
	extras := product.extras
	product = ProductSettings{}
	if err := json.Unmarshal(original, &product); err != nil {
		return s.Settings, err
	}
	product.extras = extras
	view := publicProduct(product)
	if err := mergeSettings(view, update.Settings); err != nil {
		return s.Settings, err
	}
	body, _ := json.Marshal(view)
	// 在已有值上解码，保留未提交的密钥。
	if strictJSON(bytes.NewReader(body), &product) != nil {
		return s.Settings, bad
	}
	fields := secretFields(&product)
	for name, operation := range update.Secrets {
		field, exists := fields[name]
		if !exists {
			return s.Settings, bad
		}
		switch operation.Action {
		case "keep", "clear":
			if operation.Value != nil {
				return s.Settings, bad
			}
			if operation.Action == "clear" {
				*field = nil
			}
		case "replace":
			value := configured(operation.Value)
			if value == "" || strings.ContainsAny(value, "*•●…") || len(value) > 16384 {
				return s.Settings, bad
			}
			*field = &value
		default:
			return s.Settings, bad
		}
	}
	result := product.apply(s.Settings)
	if err := result.Validate(); err != nil {
		return s.Settings, bad
	}
	return result, nil
}

func (s *Store) persistSettings(settings Settings) error {
	version := s.productVersion + 1
	oldQQ := s.Settings.Channel.botID("qq")
	retiredKeys := map[string]bool{}
	if oldQQ != "" && oldQQ != settings.Channel.botID("qq") {
		channel := s.Settings.Channel
		channel.Kind = "qq"
		retiredKeys[notificationChannelKey(s.channelForDelivery(channel))] = true
		for key, historical := range settings.notificationChannels {
			if historical.Kind == "qq" && historical.botID("qq") == oldQQ {
				retiredKeys[key] = true
				delete(settings.notificationChannels, key)
			}
		}
	}
	body, err := encryptProduct(settings, version)
	if err != nil {
		return err
	}
	err = transaction(s.state, func(tx *sql.Tx) error {
		result, err := tx.Exec(`UPDATE product_settings SET version = ?, ciphertext = ?, updated_at = ?
			WHERE instance_id = ? AND version = ?`, version, body, nowText(), settings.Memos.InstanceID, s.productVersion)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return failure("SETTINGS_CONFLICT", "Settings have changed; reload before saving", 409, false)
		}
		if configured(s.Settings.Channel.UserOpenID) != configured(settings.Channel.UserOpenID) {
			instance, user, bot := settings.Memos.InstanceID, settings.Memos.User, s.Settings.Channel.botID("feishu-app")
			if _, err := tx.Exec(`DELETE FROM bot_bindings WHERE instance_id=? AND user_name=? AND kind='feishu-app' AND bot_id=?`, instance, user, bot); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE bot_inbox SET body=NULL,phase='retired',error_code='CHANNEL_BINDING_CHANGED'
				WHERE instance_id=? AND user_name=? AND kind='feishu-app' AND bot_id=? AND phase IN ('pending','reply','sending')`, instance, user, bot); err != nil {
				return err
			}
		}
		if len(retiredKeys) > 0 {
			instance, user := settings.Memos.InstanceID, settings.Memos.User
			for _, stmt := range []string{
				`DELETE FROM bot_bindings WHERE instance_id=? AND user_name=? AND kind='qq' AND bot_id=?`,
				`DELETE FROM bot_cursors WHERE instance_id=? AND user_name=? AND kind='qq' AND bot_id=?`,
				`UPDATE bot_inbox SET body=NULL,phase='retired',error_code='BOT_REPLACED' WHERE instance_id=? AND user_name=? AND kind='qq' AND bot_id=? AND phase IN ('pending','reply','sending')`,
			} {
				if _, err := tx.Exec(stmt, instance, user, oldQQ); err != nil {
					return err
				}
			}
			for key := range retiredKeys {
				if _, err := tx.Exec(`UPDATE delivery_logs SET status='failed',last_error='BOT_REPLACED' WHERE instance_id=? AND user_name=? AND channel_key=? AND status IN ('pending','running','failed')`, instance, user, key); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		s.productVersion = version
	}
	return err
}
