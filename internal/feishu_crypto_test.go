package moerise

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// Callback replay tests do not cover queue identity binding or credential rotation.
func TestFeishuQueueEncryptionBindsIdentityAndSurvivesAppSecretRotation(t *testing.T) {
	settings := feishuSettings(t)
	service := &FeishuService{Store: &Store{Settings: settings}}
	const message = "om_queue"
	body := []byte("原样保存的正文 #标签")
	sealed, err := service.seal(message, body)
	if err != nil {
		t.Fatal(err)
	}
	service.Store.Settings.Channel.AppSecret = stringPointer("rotated-application-secret")
	plain, err := service.unseal(message, sealed)
	if err != nil || !bytes.Equal(plain, body) {
		t.Fatalf("application secret rotation changed the queue: %v", err)
	}
	for _, binding := range []string{"instance", "user", "app", "sender", "message", "key", "ciphertext", "truncated"} {
		t.Run(binding, func(t *testing.T) {
			other := &FeishuService{Store: &Store{Settings: service.Store.Settings}}
			id, ciphertext := message, bytes.Clone(sealed)
			switch binding {
			case "instance":
				other.Store.Settings.Memos.InstanceID = "other-instance"
			case "user":
				other.Store.Settings.Memos.User = "users/other"
			case "app":
				other.Store.Settings.Channel.AppID = stringPointer("cli_other")
			case "sender":
				other.Store.Settings.Channel.UserOpenID = stringPointer("ou_other")
			case "message":
				id = "om_other"
			case "key":
				other.Store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{32}, 32))
			case "ciphertext":
				ciphertext[len(ciphertext)-1] ^= 1
			case "truncated":
				ciphertext = ciphertext[:3]
			}
			if _, err := other.unseal(id, ciphertext); errorCode(err) != "CHANNEL_QUEUE_KEY" {
				t.Fatalf("queue accepted a different %s: %v", binding, err)
			}
		})
	}
	service.Store.Settings.Web.StateEncryptionKey = ""
	if _, err := service.seal(message, body); err == nil {
		t.Fatal("queue accepted a missing state encryption key")
	}
}
