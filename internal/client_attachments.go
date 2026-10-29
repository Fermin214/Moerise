package moerise

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const browserAttachmentLimit int64 = 128 << 20

var byteRangePattern = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)

type browserAttachmentSource interface {
	GetAttachment(context.Context, string) (map[string]any, error)
	OpenAttachment(context.Context, map[string]any, string, string) (*http.Response, error)
}

func validByteRange(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 || !byteRangePattern.MatchString(value) {
		return false
	}
	start, end, _ := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if start == "" {
		n, err := strconv.ParseInt(end, 10, 64)
		return err == nil && n > 0
	}
	first, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		return false
	}
	if end != "" {
		last, err := strconv.ParseInt(end, 10, 64)
		return err == nil && last >= first
	}
	return true
}

func (m *MemosClient) OpenAttachment(ctx context.Context, file map[string]any, method, byteRange string) (*http.Response, error) {
	name, _ := file["name"].(string)
	filename, _ := file["filename"].(string)
	if !attachmentPattern.MatchString(name) || filename == "" ||
		(method != http.MethodGet && method != http.MethodHead) || !validByteRange(byteRange) {
		return nil, failure("INVALID_ARGUMENT", "Invalid attachment request", 422, false)
	}
	// Memos 0.31 的文件路由只注册 GET；HEAD 复用该响应头并立即关闭正文。
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.config.BaseURL+"/file/"+name+"/"+url.PathEscape(filename), nil)
	if err != nil {
		return nil, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos attachment path")
	}
	request.Header.Set("Authorization", "Bearer "+m.config.Token)
	request.Header.Set("Accept-Encoding", "identity")
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return nil, upstreamError("UPSTREAM_UNAVAILABLE", "Memos attachment is unavailable")
	}
	switch response.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		if response.ContentLength > browserAttachmentLimit {
			response.Body.Close()
			return nil, failure("ATTACHMENT_SIZE", "Attachment exceeds the 128 MiB download limit", 422, false)
		}
		return response, nil
	}
	response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, failure("SOURCE_NOT_FOUND", "Attachment is unavailable", 404, false)
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, upstreamError("UPSTREAM_AUTH", "Memos rejected the configured credential")
	}
	return nil, upstreamError("UPSTREAM_UNAVAILABLE", "Memos attachment is unavailable")
}

func (a *ClientAPI) serveAttachment(w http.ResponseWriter, r *http.Request, name, attachmentName string) error {
	if !memoPattern.MatchString(name) || !attachmentPattern.MatchString(attachmentName) {
		return failure("INVALID_RESOURCE", "Invalid attachment resource", 422, false)
	}
	if len(r.Header.Values("Range")) > 1 || !validByteRange(r.Header.Get("Range")) {
		return failure("INVALID_RANGE", "Only one valid byte range is supported", 416, false)
	}
	notes := a.app.Notes
	if err := notes.Verify(r.Context()); err != nil {
		return err
	}
	parent, err := notes.fresh(r.Context(), name)
	if err != nil {
		return err
	}
	if parent == nil {
		return failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	var linked map[string]any
	for _, item := range parent.Attachments {
		if item["name"] == attachmentName {
			linked = item
			break
		}
	}
	if linked == nil {
		return failure("SOURCE_NOT_FOUND", "Attachment is unavailable", 404, false)
	}
	source, ok := notes.Source.(browserAttachmentSource)
	if !ok {
		return upstreamError("UPSTREAM_PROTOCOL", "The source does not support file streaming")
	}
	file, err := source.GetAttachment(r.Context(), attachmentName)
	if err != nil {
		return err
	}
	if file == nil || file["name"] != attachmentName || file["memo"] != name ||
		file["filename"] != linked["filename"] || file["type"] != linked["type"] {
		return failure("SOURCE_NOT_FOUND", "Attachment is unavailable", 404, false)
	}
	response, err := source.OpenAttachment(r.Context(), file, r.Method, r.Header.Get("Range"))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// 发送响应头前再检查父笔记和身份，失败时不输出部分私人文件。
	fresh, err := notes.fresh(r.Context(), name)
	if err != nil {
		return err
	}
	if fresh == nil || fresh.SourceHash != parent.SourceHash {
		return failure("SOURCE_CHANGED", "Memo changed while opening the attachment", 409, true)
	}
	if err := notes.Verify(r.Context()); err != nil {
		return err
	}
	filename, _ := file["filename"].(string)
	kind, _ := file["type"].(string)
	disposition := "attachment"
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		disposition = "inline"
	default:
		kind = "application/octet-stream"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Accept-Ranges", "bytes")
	for _, key := range []string{"Content-Length", "Content-Range"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, err = io.Copy(w, io.LimitReader(response.Body, browserAttachmentLimit))
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		var extra [1]byte
		if n, err := response.Body.Read(extra[:]); n != 0 || (err != nil && err != io.EOF) {
			panic(http.ErrAbortHandler)
		}
	}
	return nil
}
