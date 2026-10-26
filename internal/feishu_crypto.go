package moerise

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
)

func (s *FeishuService) eventJSON(request *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return nil, failure("INVALID_ARGUMENT", "Invalid Feishu event body", 422, false)
	}
	key := configured(s.Store.Settings.Channel.EncryptKey)
	if key == "" {
		return body, nil
	}
	var envelope struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Encrypt == "" {
		return nil, failure("UNAUTHENTICATED", "Encrypted Feishu event required", 401, false)
	}
	// Feishu signs timestamp, nonce, Encrypt Key and the exact encrypted JSON body.
	stamp, nonce := request.Header.Get("X-Lark-Request-Timestamp"), request.Header.Get("X-Lark-Request-Nonce")
	signature := hashBytes([]byte(stamp + nonce + key + string(body)))
	data, err := base64.StdEncoding.DecodeString(envelope.Encrypt)
	if err != nil || len(data) < 2*aes.BlockSize || (len(data)-aes.BlockSize)%aes.BlockSize != 0 {
		return nil, failure("INVALID_ARGUMENT", "Invalid Feishu ciphertext", 422, false)
	}
	digest := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(digest[:])
	plain := make([]byte, len(data)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, data[:aes.BlockSize]).CryptBlocks(plain, data[aes.BlockSize:])
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || !bytes.Equal(plain[len(plain)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, failure("INVALID_ARGUMENT", "Invalid Feishu padding", 422, false)
	}
	plain = plain[:len(plain)-padding]
	var challenge struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(plain, &challenge)
	if challenge.Type == "url_verification" &&
		hmac.Equal([]byte(challenge.Token), []byte(configured(s.Store.Settings.Channel.VerificationToken))) {
		return plain, nil
	}
	if stamp == "" || nonce == "" || !hmac.Equal([]byte(signature), []byte(request.Header.Get("X-Lark-Signature"))) {
		return nil, failure("UNAUTHENTICATED", "Invalid Feishu signature", 401, false)
	}
	return plain, nil
}

func (s *FeishuService) queueCipher() cipher.AEAD {
	key := sha256.Sum256([]byte("moerise-feishu-events-v1\x00" + configured(s.Store.Settings.Channel.AppSecret)))
	block, _ := aes.NewCipher(key[:])
	aead, _ := cipher.NewGCM(block)
	return aead
}

func (s *FeishuService) seal(message string, body []byte) ([]byte, error) {
	aead := s.queueCipher()
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	identity := s.Store.Settings.Memos.InstanceID + "\x00" + s.Store.Settings.Memos.User + "\x00" + message
	return aead.Seal(nonce, nonce, body, []byte(identity)), nil
}

func (s *FeishuService) unseal(message string, body []byte) ([]byte, error) {
	aead := s.queueCipher()
	if len(body) < aead.NonceSize() {
		return nil, upstreamError("CHANNEL_QUEUE_KEY", "Cannot decrypt queued Feishu event")
	}
	identity := s.Store.Settings.Memos.InstanceID + "\x00" + s.Store.Settings.Memos.User + "\x00" + message
	data, err := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], []byte(identity))
	if err != nil {
		return nil, upstreamError("CHANNEL_QUEUE_KEY", "Cannot decrypt queued Feishu event; restore its application secret")
	}
	return data, nil
}
