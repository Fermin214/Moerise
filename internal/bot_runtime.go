package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type BotChannel interface {
	Run(context.Context) error
	Send(context.Context, string, string, string) (string, error)
}

type botConnection struct {
	bot            BotChannel
	id, credential string
	cancel         context.CancelFunc
	done           chan struct{}
}

func (client *botConnection) stop() {
	client.cancel()
	<-client.done
}

type botSender interface {
	Send(context.Context, string, string, string) (string, error)
}

type botTask struct {
	key, peer, message, operation, phase, reply string
	result, resource                            string
	body                                        []byte
	received, sentAt                            int64
	batch                                       sql.NullString
}

func (a *App) processBotMessage(ctx context.Context, kind, id string, sender botSender) (bool, error) {
	store := a.Store
	var task botTask
	err := store.state.QueryRowContext(ctx, `SELECT i.write_key,i.peer,i.message_id,i.operation,i.phase,i.reply,i.body,i.received_at,i.message_at,i.batch_id,i.result_kind,i.resource_name
		FROM bot_inbox i JOIN bot_bindings b USING(instance_id,user_name,kind,bot_id,peer)
		WHERE i.instance_id=? AND i.user_name=? AND i.kind=? AND i.bot_id=? AND i.phase IN ('pending','reply') AND i.available_at<=?
		AND (i.kind!='feishu-app' OR ?='' OR i.peer=?)
		ORDER BY i.received_at,i.rowid LIMIT 1`, store.Settings.Memos.InstanceID, store.Settings.Memos.User, kind, id, time.Now().UnixMilli(), configured(store.Settings.Channel.UserOpenID), configured(store.Settings.Channel.UserOpenID)).
		Scan(&task.key, &task.peer, &task.message, &task.operation, &task.phase, &task.reply, &task.body, &task.received, &task.sentAt, &task.batch, &task.result, &task.resource)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if task.phase == "pending" {
		return true, a.prepareBotReply(ctx, task)
	}
	if kind == "qq" && !time.Now().Before(time.UnixMilli(task.sentAt).Add(5*time.Minute)) {
		_, err := store.state.Exec(`UPDATE bot_inbox SET phase='expired',error_code='REPLY_EXPIRED' WHERE write_key=? AND phase='reply'`, task.key)
		return true, err
	}
	text := task.reply
	if kind == "feishu-app" {
		text, err = a.Feishu.render(ctx, task.result, task.resource)
		if err != nil {
			return false, err
		}
	}
	if task.batch.Valid {
		batch, err := a.Reviews.GetLocal(ctx, task.batch.String)
		if err != nil {
			text = "暂时读不到笔记"
		} else {
			text = botReviewText(batch)
		}
	}
	result, err := store.state.Exec(`UPDATE bot_inbox SET phase='sending' WHERE write_key=? AND phase='reply'`, task.key)
	if err != nil {
		return false, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return true, nil
	}
	receipt, err := sender.Send(ctx, task.peer, task.message, text)
	status, code, available := "sent", "", int64(0)
	var retry *notificationRetryError
	switch {
	case errors.As(err, &retry):
		status, code, available = "reply", errorCode(err), time.Now().Add(max(retry.after, time.Second)).UnixMilli()
	case err != nil:
		code = errorCode(err)
		status = "failed"

	case receipt == "":
		status, code = "failed", "SEND_OUTCOME_UNKNOWN"
	}
	_, err = store.state.Exec(`UPDATE bot_inbox SET phase=?,receipt=?,error_code=?,available_at=? WHERE write_key=? AND phase='sending'`, status, nullable(receipt), nullable(code), available, task.key)
	return true, err
}

func (a *App) prepareBotReply(ctx context.Context, task botTask) error {
	store := a.Store
	var batchID, reply string
	switch task.operation {
	case "feishu", "feishu-legacy":
		return a.prepareFeishuReply(ctx, task)
	case "review":
		batch, err := a.Reviews.DailyLocal(ctx, time.UnixMilli(task.sentAt))
		if err != nil {
			reply = "暂时读不到笔记"
		} else {
			batchID = batch.ID
		}
	case "text":
		cipher, err := newStateCipher(store.Settings, "bot_inbox")
		if err != nil {
			return err
		}
		var body []byte
		if len(task.body) >= cipher.NonceSize()+cipher.Overhead() {
			body, err = cipher.Open(nil, task.body[:cipher.NonceSize()], task.body[cipher.NonceSize():], []byte(task.key))
		}
		if body == nil || err != nil {
			_, err = store.state.ExecContext(ctx, `UPDATE bot_inbox SET phase='failed',body=NULL,error_code='INVALID_CIPHERTEXT' WHERE write_key=? AND phase='pending'`, task.key)
			return err
		}
		_, _, err = a.Records.Create(ctx, task.key, CreateNote{Content: string(body)})
		if err != nil {
			var service *ServiceError
			if !errors.As(err, &service) || service.Retryable {
				_, saveErr := store.state.Exec(`UPDATE bot_inbox SET available_at=?,error_code=? WHERE write_key=? AND phase='pending'`,
					time.Now().Add(5*time.Second).UnixMilli(), errorCode(err), task.key)
				return saveErr
			}
			reply = "笔记未保存"
		} else {
			reply = "已保存"
		}
	case "invalid":
		reply = "请输入非空文字，正文不能超过 64 KiB"
	case "attachment":
		reply = "仅支持文字速记，附件未保存"
	default:
		reply = "不支持这种消息，未保存"
	}
	_, err := store.state.Exec(`UPDATE bot_inbox SET phase='reply',body=NULL,reply=?,batch_id=?,error_code=NULL,available_at=0 WHERE write_key=? AND phase='pending'`, reply, nullable(batchID), task.key)
	return err
}

func (a *App) prepareFeishuReply(ctx context.Context, task botTask) error {
	s := a.Store
	var body []byte
	var err error
	if task.operation == "feishu-legacy" {
		body, err = a.Feishu.unseal(task.message, task.peer, task.body)
	} else {
		cipher, e := newStateCipher(s.Settings, "bot_inbox")
		err = e
		if err == nil && len(task.body) >= cipher.NonceSize()+cipher.Overhead() {
			body, err = cipher.Open(nil, task.body[:cipher.NonceSize()], task.body[cipher.NonceSize():], []byte(task.key))
		}
	}
	if body == nil || err != nil {
		_, err = s.state.Exec(`UPDATE bot_inbox SET phase='failed',body=NULL,error_code='INVALID_CIPHERTEXT' WHERE write_key=?`, task.key)
		return err
	}
	var message feishuMessage
	err = json.Unmarshal(body, &message)
	kind, resource := "", ""
	if err == nil {
		kind, resource, err = a.Feishu.execute(ctx, message, time.UnixMilli(task.sentAt))
	}
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && !service.Retryable {
			kind, resource = "error", service.Code
			if service.Code == "INVALID_ARGUMENT" {
				if message.Type == "text" {
					kind = "invalid-text"
				} else {
					kind = "invalid-attachment"
				}
			}
		} else {
			_, err = s.state.Exec(`UPDATE bot_inbox SET available_at=?,error_code=? WHERE write_key=?`, time.Now().Add(5*time.Second).UnixMilli(), errorCode(err), task.key)
			return err
		}
	}
	_, err = s.state.Exec(`UPDATE bot_inbox SET phase='reply',body=NULL,result_kind=?,resource_name=?,available_at=0,error_code=NULL WHERE write_key=? AND phase='pending'`, kind, resource, task.key)
	return err
}

type qqDelivery struct {
	store   *Store
	reviews *ReviewService
	id      string
	peer    string
	bot     botSender
}

func newQQDelivery(channel ChannelConfig, store *Store, reviews *ReviewService) NotificationTransport {
	if !channel.botEnabled("qq") {
		return nil
	}
	id := channel.botID("qq")
	return &qqDelivery{store: store, reviews: reviews, id: id, peer: configured(channel.Destination), bot: newQQBot(id, configured(channel.QQAppSecret), store)}
}

func (d *qqDelivery) Send(ctx context.Context, item Notification) (string, error) {
	peer, _, err := d.store.botPeer("qq", d.id)
	if err != nil {
		return "", err
	}
	if peer == "" || d.peer != "" && peer != d.peer {
		return "", failure("QQ_DELIVERY_REJECTED", "QQ destination changed", 422, false)
	}
	batch, err := d.reviews.forNotification(ctx, item)
	if err != nil {
		return "", err
	}
	text := botReviewText(batch)
	if text == "今天没有可回顾的笔记" {
		return "", failure("REVIEW_EMPTY", "No readable review items", 404, false)
	}
	receipt, err := d.bot.Send(ctx, peer, "", text)
	if err == nil {
		return receipt, nil
	}
	var retry *notificationRetryError
	if errors.As(err, &retry) || strings.HasPrefix(errorCode(err), "QQ_REJECTED_") {
		return "", err
	}
	if errorCode(err) == "DELIVERY_UNKNOWN" || errorCode(err) == "SEND_OUTCOME_UNKNOWN" {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "QQ delivery outcome is unknown")
	}
	return "", failure("QQ_DELIVERY_REJECTED", errorCode(err), 422, false)
}
