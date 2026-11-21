package moerise

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type botReceiverStore struct{ store *Store }

func (r botReceiverStore) Status(kind, botID, state, detail, link string) {
	r.store.setBotStatus(kind, botID, state, detail, link)
}

func (r botReceiverStore) Cursor(ctx context.Context, kind, botID string) (botCursor, error) {
	s := r.store
	unlock := s.lockRuntime()
	defer unlock()
	var cursor botCursor
	err := s.state.QueryRowContext(ctx, `SELECT session,sequence FROM bot_cursors WHERE instance_id=? AND user_name=? AND kind=? AND bot_id=?`, s.Settings.Memos.InstanceID, s.Settings.Memos.User, kind, botID).Scan(&cursor.Session, &cursor.Sequence)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return cursor, err
}

func botTextCommand(text string) (string, string) {
	switch text {
	case "/回顾", "/review":
		return "review", ""
	case "//回顾", "//review":
		return "text", text[1:]
	}
	if strings.TrimSpace(text) == "" || len(text) > 65536 {
		return "invalid", ""
	}
	return "text", text
}

func botReviewText(batch ReviewBatch) string {
	var excerpts []string
	count := 0
	for _, item := range batch.Items {
		if item.Unavailable || item.Note == nil || !reviewContent(item.Note.Content, item.Note.Attachments) {
			continue
		}
		count++
		if len(excerpts) == 3 {
			continue
		}
		line := []rune(strings.Join(strings.Fields(item.Note.Content), " "))
		if len(line) > 80 {
			line = append(line[:79], '…')
		}
		if len(line) == 0 {
			line = []rune("图片笔记")
		}
		excerpts = append(excerpts, "• "+string(line))
	}
	if count == 0 {
		return "今天没有可回顾的笔记"
	}
	return fmt.Sprintf("%s · %d 条笔记\n%s", batch.Date, count, strings.Join(excerpts, "\n"))
}

func (r botReceiverStore) Accept(ctx context.Context, kind, botID string, cursor botCursor, msg *botMessage) error {
	s := r.store
	unlock := s.lockRuntime()
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.Settings.Channel.botEnabled(kind) || s.Settings.Channel.botID(kind) != botID {
		return context.Canceled
	}
	instance, user := s.Settings.Memos.InstanceID, s.Settings.Memos.User
	err := transactionContext(ctx, s.state, func(tx *sql.Tx) error {
		if msg != nil {
			if err := s.acceptBotMessage(tx, kind, botID, *msg); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO bot_cursors VALUES (?,?,?,?,?,?) ON CONFLICT(instance_id,user_name,kind,bot_id)
			DO UPDATE SET session=excluded.session,sequence=excluded.sequence`, instance, user, kind, botID, cursor.Session, cursor.Sequence)
		return err
	})
	if err == nil {
		s.wakeChannels()
	}
	return err
}

func (s *Store) acceptBotMessage(tx *sql.Tx, kind, botID string, msg botMessage) error {
	if msg.Peer == "" || msg.ID == "" || msg.At.IsZero() {
		return nil
	}
	instance, user := s.Settings.Memos.InstanceID, s.Settings.Memos.User
	var restored int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT restored_at FROM bot_restore WHERE instance_id=?),0)`, instance).Scan(&restored); err != nil {
		return err
	}
	if msg.At.UnixMilli() <= restored {
		return nil
	}
	var peer string
	var err error
	if msg.Bind {
		if kind == "feishu-app" && configured(s.Settings.Channel.UserOpenID) == msg.Peer {
			if _, err = tx.Exec(`INSERT INTO bot_bindings VALUES(?,?,?,?,?,?) ON CONFLICT(instance_id,user_name,kind,bot_id) DO UPDATE SET peer=excluded.peer,bound_at=excluded.bound_at WHERE peer!=excluded.peer`, instance, user, kind, botID, msg.Peer, time.Now().UnixMilli()); err != nil {
				return err
			}
		}
		peer, err = s.bindBotPeer(tx, kind, botID, msg.Peer, time.Now())
	} else {
		err = tx.QueryRow(`SELECT peer FROM bot_bindings WHERE instance_id=? AND user_name=? AND kind=? AND bot_id=?`, instance, user, kind, botID).Scan(&peer)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if peer != msg.Peer || msg.Kind == "start" {
		return nil
	}
	digest := msg.Digest
	if digest == "" {
		digest = hashBytes(canonicalJSON([]string{msg.Kind, msg.Text}))
	}
	var savedKey, savedDigest, savedIndex string
	err = tx.QueryRow(`SELECT write_key,digest,msg_index FROM bot_inbox WHERE instance_id=? AND user_name=? AND kind=? AND bot_id=? AND peer=? AND message_id=?`,
		instance, user, kind, botID, peer, msg.ID).Scan(&savedKey, &savedDigest, &savedIndex)
	if err == nil {
		if savedDigest != digest || kind != "feishu-app" && savedIndex != "" && msg.Index != "" && savedIndex != msg.Index {
			if kind == "feishu-app" {
				return failure("IDEMPOTENCY_CONFLICT", "Feishu message payload changed", 409, false)
			}
			_, err = tx.Exec(`UPDATE bot_inbox SET phase='conflict',body=NULL,error_code='MESSAGE_CONFLICT' WHERE write_key=?`, savedKey)
		} else if savedIndex == "" && msg.Index != "" {
			_, err = tx.Exec(`UPDATE bot_inbox SET msg_index=? WHERE write_key=?`, msg.Index, savedKey)
		}
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	key := kind + ":" + newID()
	op, text := msg.Kind, msg.Text
	var body []byte
	if op == "text" {
		op, text = botTextCommand(text)
	}
	if op == "text" || op == "feishu" {
		cipher, err := newStateCipher(s.Settings, "bot_inbox")
		if err != nil {
			return err
		}
		nonce := make([]byte, cipher.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		body = cipher.Seal(nonce, nonce, []byte(text), []byte(key))
	}
	_, err = tx.Exec(`INSERT INTO bot_inbox (write_key,instance_id,user_name,kind,bot_id,peer,message_id,msg_index,digest,operation,body,received_at,message_at,phase)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'pending')`, key, instance, user, kind, botID, peer, msg.ID, msg.Index, digest, op, body, time.Now().UnixMilli(), msg.At.UnixMilli())
	return err
}
