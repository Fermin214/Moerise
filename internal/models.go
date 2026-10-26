package moerise

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type ServiceError struct {
	Code      string
	Message   string
	Status    int
	Retryable bool
}

func (e *ServiceError) Error() string { return e.Message }

func failure(code, message string, status int, retryable bool) error {
	return &ServiceError{code, message, status, retryable}
}

func upstreamError(code, message string) error {
	return failure(code, message, http.StatusServiceUnavailable, true)
}

func errorCode(err error) string {
	var service *ServiceError
	if errors.As(err, &service) {
		return service.Code
	}
	return "SYNC_INTERNAL"
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic("cannot obtain operating system randomness")
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:])
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func nowText() string     { return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z") }
func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type Memo struct {
	Name        string           `json:"name"`
	Creator     string           `json:"creator"`
	Content     string           `json:"content"`
	State       string           `json:"state"`
	Visibility  string           `json:"visibility"`
	CreateTime  string           `json:"create_time"`
	UpdateTime  string           `json:"update_time"`
	Tags        []string         `json:"tags"`
	Attachments []map[string]any `json:"attachments"`
	Relations   []map[string]any `json:"relations"`
	Pinned      bool             `json:"pinned"`
	SourceHash  string           `json:"source_hash"`
	ContentHash string           `json:"content_hash"`
}

type Note struct {
	InstanceID  string           `json:"instance_id"`
	Name        string           `json:"name"`
	Creator     string           `json:"creator"`
	State       string           `json:"state"`
	Visibility  string           `json:"visibility"`
	Content     string           `json:"content"`
	Tags        []string         `json:"tags"`
	Attachments []map[string]any `json:"attachments"`
	CreateTime  string           `json:"create_time"`
	UpdateTime  string           `json:"update_time"`
	SourceHash  string           `json:"source_hash"`
	URL         string           `json:"url"`
}

type CreateNote struct {
	Content     string             `json:"content"`
	Tags        []string           `json:"tags"`
	Attachments []CreateAttachment `json:"attachments,omitempty"`
}

type CreateAttachment struct {
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Content  []byte `json:"content"`
}

func (m Memo) Note(settings Settings) Note {
	return Note{settings.Memos.InstanceID, m.Name, m.Creator, m.State, m.Visibility,
		m.Content, m.Tags, m.Attachments, m.CreateTime, m.UpdateTime, m.SourceHash,
		settings.Memos.PublicURL + "/" + m.Name}
}

type Job struct {
	ID          string         `json:"id"`
	InstanceID  string         `json:"instance_id"`
	Kind        string         `json:"kind"`
	Mode        string         `json:"mode"`
	Status      string         `json:"status"`
	Attempts    int            `json:"attempts"`
	AvailableAt float64        `json:"available_at"`
	LeaseUntil  *float64       `json:"lease_until"`
	CreatedAt   string         `json:"created_at"`
	StartedAt   *string        `json:"started_at"`
	CompletedAt *string        `json:"completed_at"`
	LastError   *string        `json:"last_error"`
	Result      map[string]any `json:"result"`
}

type JobCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type SyncStatus struct {
	InstanceID      string         `json:"instance_id"`
	Watermark       *string        `json:"watermark"`
	LastIncremental *string        `json:"last_incremental"`
	LastFull        *string        `json:"last_full"`
	SourceOK        bool           `json:"source_ok"`
	IndexGeneration string         `json:"index_generation"`
	UpstreamVersion *string        `json:"upstream_version"`
	LastError       *string        `json:"last_error"`
	UpdatedAt       string         `json:"updated_at"`
	Counts          map[string]int `json:"counts"`
	Jobs            []JobCount     `json:"jobs"`
}

func decodeMemo(data []byte) (Memo, error) {
	var memo Memo
	err := json.Unmarshal(data, &memo)
	return memo, err
}
