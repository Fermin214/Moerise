package moerise

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

func derivedID(secret string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.Join(parts, "\x00")))
	data := mac.Sum(nil)[:16]
	data[6], data[8] = data[6]&0x0f|0x80, data[8]&0x3f|0x80
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

type SearchRequest struct {
	Query           string   `json:"query" jsonschema:"Search text, at most 500 Unicode code points"`
	Mode            string   `json:"mode,omitempty" jsonschema:"hybrid (default), keyword, or semantic"`
	Limit           int      `json:"limit,omitempty" jsonschema:"Result count from 1 to 20; defaults to 10"`
	Tags            []string `json:"tags,omitempty" jsonschema:"Require all tags, including their child tags"`
	IncludeArchived bool     `json:"include_archived,omitempty"`
}

type SearchSnippet struct {
	Text       string `json:"text"`
	StartRune  int    `json:"start_rune"`
	EndRune    int    `json:"end_rune"`
	SourceHash string `json:"source_hash"`
}

type SearchHit struct {
	Note       Note          `json:"note"`
	Snippet    SearchSnippet `json:"snippet"`
	Score      float64       `json:"score"`
	Signals    []string      `json:"signals"`
	Similarity *float64      `json:"similarity,omitempty"`
}

type SearchResponse struct {
	Results  []SearchHit `json:"results"`
	Mode     string      `json:"mode"`
	Warnings []string    `json:"warnings"`
}

type RelatedRequest struct {
	Name            string `json:"name" jsonschema:"Source resource, such as memos/abc"`
	Limit           int    `json:"limit,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
}

type WalkRequest struct {
	Name            string   `json:"name,omitempty" jsonschema:"Current memo; optional for a random start"`
	Mode            string   `json:"mode,omitempty" jsonschema:"continue (default), angle, or random"`
	Path            []string `json:"path,omitempty" jsonschema:"Already visited resource names, at most 100"`
	IncludeArchived bool     `json:"include_archived,omitempty"`
}

type WalkResponse struct {
	Mode      string     `json:"mode"`
	Item      *SearchHit `json:"item"`
	Path      []string   `json:"path"`
	Exhausted bool       `json:"exhausted"`
	Warnings  []string   `json:"warnings"`
}

type SearchIndexStatus struct {
	Enabled      bool    `json:"enabled"`
	SpaceID      string  `json:"space_id,omitempty"`
	Model        string  `json:"model,omitempty"`
	Dimensions   int     `json:"dimensions"`
	ChunkVersion string  `json:"chunk_version"`
	Status       string  `json:"status"`
	Chunks       int     `json:"chunks"`
	PendingMemos int     `json:"pending_memos"`
	VectorBytes  int64   `json:"vector_bytes"`
	LastError    *string `json:"last_error"`
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
