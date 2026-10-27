//go:build integration

package acceptance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	moerise "moerise/internal"
)

const stage4DatasetSHA256 = "4f1144482bc645bd3b5a07001ee00a606d4f99e6b0ff571cc08a5fb00f7c5d4d"

type stage4Note struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	CreateTime string `json:"create_time"`
	UpdateTime string `json:"update_time"`
	State      string `json:"state"`
}

type stage4Expected struct {
	Sources    []string `json:"sources"`
	Keywords   []string `json:"keywords"`
	Status     string   `json:"status"`
	Prohibited []string `json:"prohibited"`
}

type stage4Case struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Request  moerise.AIRequest `json:"request"`
	Expected stage4Expected    `json:"expected"`
	Checks   []string          `json:"checks"`
}

type stage4Dataset struct {
	Version int          `json:"version"`
	Notes   []stage4Note `json:"notes"`
	Cases   []stage4Case `json:"cases"`
}

type stage4PublicError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	RequestID string `json:"request_id"`
}

type stage4StreamEvent struct {
	Type       string          `json:"type"`
	ReceivedMS float64         `json:"received_ms"`
	Data       json.RawMessage `json:"data"`
}

type stage4ClientMetrics struct {
	ReferencesMS   *float64 `json:"references_ms"`
	FirstContentMS *float64 `json:"first_content_ms"`
	TotalMS        float64  `json:"total_ms"`
}

type stage4Run struct {
	Case             stage4Case            `json:"case"`
	Phase            string                `json:"phase"`
	Concurrency      int                   `json:"concurrency"`
	Endpoint         string                `json:"endpoint"`
	Request          moerise.AIRequest     `json:"request"`
	StartedAt        string                `json:"started_at"`
	HTTPStatus       int                   `json:"http_status"`
	RequestID        string                `json:"request_id"`
	Events           []stage4StreamEvent   `json:"events"`
	References       []moerise.Evidence    `json:"references"`
	Content          []moerise.AIStatement `json:"content"`
	Result           *moerise.AIResult     `json:"result"`
	Error            *stage4PublicError    `json:"error"`
	TransportError   string                `json:"transport_error,omitempty"`
	TrailingData     string                `json:"trailing_data,omitempty"`
	ProtocolErrors   []string              `json:"protocol_errors"`
	HardFailures     []string              `json:"hard_failures"`
	HardInvariantsOK bool                  `json:"hard_invariants_ok"`
	ManualReview     bool                  `json:"manual_review_required"`
	ClientMetrics    stage4ClientMetrics   `json:"client_metrics"`
	Metrics          *moerise.AIMetrics    `json:"metrics"`
	Usage            moerise.LLMUsage      `json:"usage"`
	Process          *stage4ProcessSample  `json:"process,omitempty"`
}

type stage4ProcessSample struct {
	PID             int             `json:"pid"`
	Before          stage3Resources `json:"before"`
	After           stage3Resources `json:"after"`
	CPUSecondsDelta float64         `json:"cpu_seconds_delta"`
}

type stage4ConcurrencyRun struct {
	Concurrency int                  `json:"concurrency"`
	CaseID      string               `json:"case_id"`
	WallMS      float64              `json:"wall_ms"`
	Process     *stage4ProcessSample `json:"process"`
	Requests    []stage4Run          `json:"requests"`
}

type stage4Report struct {
	Version         int                    `json:"version"`
	DatasetSHA256   string                 `json:"dataset_sha256"`
	DatasetNotes    int                    `json:"dataset_notes"`
	DatasetCases    int                    `json:"dataset_cases"`
	SelectedCases   []string               `json:"selected_cases"`
	StartedAt       string                 `json:"started_at"`
	FinishedAt      string                 `json:"finished_at,omitempty"`
	Execution       string                 `json:"execution"`
	ManualReview    bool                   `json:"manual_review_required"`
	ReviewPolicy    string                 `json:"review_policy"`
	LLM             map[string]any         `json:"llm"`
	Retrieval       map[string]any         `json:"retrieval"`
	Environment     map[string]any         `json:"environment"`
	Sources         []moerise.Note         `json:"sources"`
	Runs            []stage4Run            `json:"runs"`
	ConcurrencyRuns []stage4ConcurrencyRun `json:"concurrency_runs"`
	Completed       int                    `json:"completed_requests"`
	ExecutionErrors int                    `json:"execution_errors"`
	HardFailures    int                    `json:"hard_failure_requests"`
}

// CONFIG and OUTPUT opt in to paid calls. CASES accepts comma-separated IDs.
// CONCURRENCY=true (or 1) adds one batch at each of 1/2/4; "2,4" selects batches.
func TestStage4LiveAcceptance(t *testing.T) {
	configPath, output := os.Getenv("MOERISE_STAGE4_CONFIG"), os.Getenv("MOERISE_STAGE4_OUTPUT")
	if configPath == "" && output == "" {
		t.Skip("set MOERISE_STAGE4_CONFIG and MOERISE_STAGE4_OUTPUT for live LLM evaluation")
	}
	if configPath == "" || output == "" || runtime.GOOS != "linux" {
		t.Fatal("live evaluation requires Linux, MOERISE_STAGE4_CONFIG and MOERISE_STAGE4_OUTPUT")
	}
	config, err := moerise.LoadSettings(configPath)
	if err != nil || !config.LLM.Enabled {
		t.Fatal("live evaluation requires a valid configuration with LLM enabled")
	}
	data, err := os.ReadFile("testdata/stage4-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != stage4DatasetSHA256 {
		t.Fatal("stage4 dataset differs from its frozen SHA256")
	}
	var dataset stage4Dataset
	if err := json.Unmarshal(data, &dataset); err != nil || dataset.Version != 1 ||
		len(dataset.Notes) != 34 || len(dataset.Cases) != 40 {
		t.Fatal("invalid frozen stage4 dataset")
	}
	selected, err := stage4SelectCases(dataset.Cases, os.Getenv("MOERISE_STAGE4_CASES"))
	if err != nil {
		t.Fatal(err)
	}
	levels, err := stage4Concurrency(os.Getenv("MOERISE_STAGE4_CONCURRENCY"))
	if err != nil {
		t.Fatal(err)
	}
	timeout := max(180*time.Second, time.Duration(config.LLM.TimeoutSeconds*float64(time.Second))+30*time.Second)
	report := stage4Report{
		Version: 1, DatasetSHA256: stage4DatasetSHA256, DatasetNotes: len(dataset.Notes), DatasetCases: len(dataset.Cases),
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Execution: "setup", ManualReview: true,
		ReviewPolicy: "Review every expected source, key conclusion, prohibited inference and check manually. No semantic score is computed.",
		LLM: map[string]any{
			"protocol": config.LLM.Protocol, "model": config.LLM.Model, "timeout_seconds": config.LLM.TimeoutSeconds,
			"max_concurrency": config.LLM.MaxConcurrency, "max_retries": config.LLM.MaxRetries,
			"max_output_tokens": config.LLM.MaxOutputTokens, "context_bytes": config.LLM.ContextBytes,
		},
		Retrieval: map[string]any{
			"embedding_enabled": false, "mode": "keyword_and_tags", "degradation": "EMBEDDING_DISABLED",
			"upstream_pacing_ms": 125, "pacing_included_in_latency": true,
		},
		Environment: map[string]any{
			"go_version": runtime.Version(), "goarch": runtime.GOARCH, "cpu_count": runtime.NumCPU(),
			"memos_image": memosImage, "http_timeout_seconds": timeout.Seconds(),
			"memos_content_length_limit": 262144,
		},
		Sources: []moerise.Note{}, Runs: []stage4Run{}, ConcurrencyRuns: []stage4ConcurrencyRun{},
	}
	for _, item := range selected {
		report.SelectedCases = append(report.SelectedCases, item.ID)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	reserved, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("use a new report path: ", err)
	}
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	save := func() {
		t.Helper()
		if err := stage4SaveReport(output, report); err != nil {
			t.Fatal("write stage4 evidence report: ", err)
		}
	}
	save()
	t.Cleanup(func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Execution = "complete"
		if t.Failed() {
			report.Execution = "failed"
		}
		if err := stage4SaveReport(output, report); err != nil {
			t.Errorf("write final stage4 report: %v", err)
		}
	})

	// Pace all corpus and service source reads, including concurrent proof checks.
	// This stays below Memos' per-user budget without changing source verification.
	pace := time.NewTicker(125 * time.Millisecond)
	defer pace.Stop()
	h := newHarnessWithUpstream(t, func(address string) string {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-pace.C:
				proxy.ServeHTTP(w, r)
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(server.Close)
		return server.URL
	})
	h.client.Timeout = timeout
	h.stop()
	upstream := h.settings.Memos.BaseURL
	// v0.31.0 defaults to 8 KiB; the frozen budget case deliberately exceeds it.
	setting := h.call(upstream, "/api/v1/instance/settings/MEMO_RELATED", "GET", h.upstream, nil, 200)
	memoSetting, ok := setting["memoRelatedSetting"].(map[string]any)
	if !ok {
		t.Fatal("Memos did not return memoRelatedSetting")
	}
	memoSetting["contentLengthLimit"] = 262144
	h.call(upstream, "/api/v1/instance/settings/MEMO_RELATED", "PATCH", h.upstream, setting, 200)
	for _, note := range dataset.Notes {
		name := "memos/" + note.ID
		created := h.call(upstream, "/api/v1/memos?memoId="+url.QueryEscape(note.ID), "POST", h.upstream,
			map[string]string{"content": note.Content, "visibility": "PRIVATE"}, 200)
		if created["name"] != name {
			t.Fatal("Memos ignored the fixed memoId: ", note.ID)
		}
		// The public state field maps to store.RowStatus; row_status is not an API field.
		h.call(upstream, "/api/v1/"+name+"?updateMask=create_time,update_time,state", "PATCH", h.upstream,
			map[string]string{"name": name, "createTime": note.CreateTime, "updateTime": note.UpdateTime, "state": note.State}, 200)
		fresh := h.call(upstream, "/api/v1/"+name, "GET", h.upstream, nil, 200)
		if fresh["name"] != name || fresh["creator"] != h.settings.Memos.User ||
			fresh["content"] != note.Content || fresh["state"] != note.State ||
			!stage4SameTime(fresh["createTime"], note.CreateTime) || !stage4SameTime(fresh["updateTime"], note.UpdateTime) {
			t.Fatal("Memos import differs from the frozen source: ", note.ID)
		}
	}
	h.settings.LLM, h.settings.Embedding.Enabled = config.LLM, false
	h.settings.Memos.PageSize = 100
	h.settings.Sync.PollSeconds, h.settings.Sync.FullSeconds = 3600, 86400
	for i := range h.settings.AccessTokens {
		if h.settings.AccessTokens[i].Token == h.reader {
			h.settings.AccessTokens[i].Scopes = append(h.settings.AccessTokens[i].Scopes, "ai:generate")
		}
	}
	encoded, err := json.Marshal(h.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	h.sync("full")
	baseline := map[string]moerise.Note{}
	for _, note := range dataset.Notes {
		raw := h.call(h.base, "/api/ext/v1/notes/"+note.ID, "GET", h.reader, nil, 200)
		data, err := json.Marshal(raw)
		var fresh moerise.Note
		if err != nil || json.Unmarshal(data, &fresh) != nil || fresh.Name != "memos/"+note.ID ||
			fresh.Creator != h.settings.Memos.User || fresh.Content != note.Content ||
			fresh.State != note.State || fresh.Visibility != "PRIVATE" || fresh.SourceHash == "" ||
			fresh.URL != h.settings.Memos.PublicURL+"/"+fresh.Name ||
			!stage4SameTime(fresh.CreateTime, note.CreateTime) || !stage4SameTime(fresh.UpdateTime, note.UpdateTime) {
			t.Fatal("service source snapshot differs from imported note: ", note.ID)
		}
		baseline[fresh.Name] = fresh
		report.Sources = append(report.Sources, fresh)
	}
	pid := h.process.Process.Pid
	report.Environment["service_pid"] = pid
	report.Execution = "running"
	save()
	record := func(row stage4Run) {
		t.Helper()
		if row.Result != nil && row.Result.Completed {
			report.Completed++
		}
		if row.Error != nil || row.TransportError != "" || row.HTTPStatus != http.StatusOK || row.Result == nil {
			report.ExecutionErrors++
		}
		if !row.HardInvariantsOK {
			report.HardFailures++
		}
	}
	for _, item := range selected {
		before := stage3Process(t, pid)
		row := stage4Request(h.client, h.base, h.reader, item, "cases", 1, baseline)
		record(row)
		report.Runs = append(report.Runs, row)
		// Persist the response before /proc sampling, which can fail if serve exited.
		save()
		after := stage3Process(t, pid)
		report.Runs[len(report.Runs)-1].Process = &stage4ProcessSample{pid, before, after, after.CPUSeconds - before.CPUSeconds}
		save()
		t.Logf("stage4 %s: HTTP=%d complete=%t hard_failures=%v", item.ID, row.HTTPStatus,
			row.Result != nil && row.Result.Completed, row.HardFailures)
	}
	// One batch per requested level, using the same non-empty case for comparison.
	if len(levels) > 0 {
		var performanceCase *stage4Case
		for i := range selected {
			if len(selected[i].Request.Names) > 0 && selected[i].Expected.Status == "answered" {
				performanceCase = &selected[i]
				break
			}
		}
		if performanceCase == nil {
			performanceCase = &selected[0]
		}
		for _, level := range levels {
			batch := stage4ConcurrencyRun{Concurrency: level, CaseID: performanceCase.ID, Requests: []stage4Run{}}
			report.ConcurrencyRuns = append(report.ConcurrencyRuns, batch)
			index := len(report.ConcurrencyRuns) - 1
			before, start := stage3Process(t, pid), time.Now()
			results := make(chan stage4Run, level)
			for range level {
				go func() {
					results <- stage4Request(h.client, h.base, h.reader, *performanceCase, "concurrency", level, baseline)
				}()
			}
			for range level {
				row := <-results
				record(row)
				report.ConcurrencyRuns[index].Requests = append(report.ConcurrencyRuns[index].Requests, row)
				save()
			}
			after := stage3Process(t, pid)
			report.ConcurrencyRuns[index].WallMS = time.Since(start).Seconds() * 1000
			report.ConcurrencyRuns[index].Process = &stage4ProcessSample{pid, before, after, after.CPUSeconds - before.CPUSeconds}
			save()
		}
	}
	if report.ExecutionErrors > 0 || report.HardFailures > 0 {
		t.Errorf("stage4 execution errors=%d, hard-failure requests=%d; evidence: %s",
			report.ExecutionErrors, report.HardFailures, output)
	}
}

func stage4SaveReport(path string, report stage4Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".stage4-report-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func stage4SelectCases(all []stage4Case, filter string) ([]stage4Case, error) {
	if strings.TrimSpace(filter) == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, id := range strings.Split(filter, ",") {
		id = strings.TrimSpace(id)
		if id == "" || wanted[id] {
			return nil, fmt.Errorf("empty or duplicate stage4 case ID")
		}
		wanted[id] = true
	}
	selected := []stage4Case{}
	for _, item := range all {
		if wanted[item.ID] {
			selected = append(selected, item)
			delete(wanted, item.ID)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("unknown stage4 case IDs: %v", wanted)
	}
	return selected, nil
}

func stage4Concurrency(value string) ([]int, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "false", "0":
		return nil, nil
	case "true", "1":
		return []int{1, 2, 4}, nil
	}
	result := []int{}
	for _, part := range strings.Split(value, ",") {
		level, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || !slices.Contains([]int{1, 2, 4}, level) || slices.Contains(result, level) {
			return nil, fmt.Errorf("MOERISE_STAGE4_CONCURRENCY must be true, false or distinct levels from 1,2,4")
		}
		result = append(result, level)
	}
	return result, nil
}

func stage4SameTime(value any, expected string) bool {
	text, ok := value.(string)
	a, errA := time.Parse(time.RFC3339Nano, text)
	b, errB := time.Parse(time.RFC3339Nano, expected)
	return ok && errA == nil && errB == nil && a.Equal(b)
}

func stage4Request(client *http.Client, base, token string, item stage4Case, phase string, concurrency int, sources map[string]moerise.Note) (row stage4Run) {
	start := time.Now()
	row = stage4Run{
		Case: item, Phase: phase, Concurrency: concurrency, Endpoint: "/api/ext/v1/answers",
		Request: item.Request, StartedAt: start.UTC().Format(time.RFC3339Nano), ManualReview: true,
		Events: []stage4StreamEvent{}, References: []moerise.Evidence{}, Content: []moerise.AIStatement{},
		ProtocolErrors: []string{}, HardFailures: []string{},
	}
	if len(item.Request.Names) > 0 {
		row.Endpoint = "/api/ext/v1/insights"
	}
	row.Request.Stream = true
	defer func() {
		row.ClientMetrics.TotalMS = time.Since(start).Seconds() * 1000
		stage4Validate(&row, sources)
	}()
	data, err := json.Marshal(row.Request)
	if err != nil {
		row.TransportError = err.Error()
		return
	}
	request, err := http.NewRequest(http.MethodPost, base+row.Endpoint, bytes.NewReader(data))
	if err != nil {
		row.TransportError = err.Error()
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		row.TransportError = err.Error()
		return
	}
	defer response.Body.Close()
	row.HTTPStatus, row.RequestID = response.StatusCode, response.Header.Get("X-Request-ID")
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK {
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
		if err != nil {
			row.TransportError = err.Error()
		}
		var body struct {
			Error *stage4PublicError `json:"error"`
		}
		if len(data) > 1<<20 || !utf8.Valid(data) || json.Unmarshal(data, &body) != nil || body.Error == nil {
			row.ProtocolErrors = append(row.ProtocolErrors, "invalid HTTP error envelope")
		} else {
			row.Error = body.Error
		}
		return
	}
	if mediaErr != nil || media != "text/event-stream" {
		row.ProtocolErrors = append(row.ProtocolErrors, "expected text/event-stream")
		return
	}
	const bodyLimit = 8 << 20
	reader := &io.LimitedReader{R: response.Body, N: bodyLimit + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	event, payload, terminal, references := "", []string{}, false, false
	dispatch := func() {
		if len(payload) == 0 && event == "" {
			return
		}
		data := []byte(strings.Join(payload, "\n"))
		elapsed := time.Since(start).Seconds() * 1000
		if !utf8.Valid(data) || !json.Valid(data) {
			row.ProtocolErrors = append(row.ProtocolErrors, "invalid UTF-8 or JSON event")
			return
		}
		row.Events = append(row.Events, stage4StreamEvent{event, elapsed, append(json.RawMessage(nil), data...)})
		if terminal {
			row.ProtocolErrors = append(row.ProtocolErrors, "event after terminal event")
			return
		}
		var decoded struct {
			Type       string               `json:"type"`
			References []moerise.Evidence   `json:"references"`
			Content    *moerise.AIStatement `json:"content"`
			Result     *moerise.AIResult    `json:"result"`
			Error      *stage4PublicError   `json:"error"`
			Completed  bool                 `json:"completed"`
		}
		if json.Unmarshal(data, &decoded) != nil || decoded.Type != event {
			row.ProtocolErrors = append(row.ProtocolErrors, "event/type mismatch")
			return
		}
		switch event {
		case "references":
			if references || len(row.Content) != 0 || decoded.References == nil {
				row.ProtocolErrors = append(row.ProtocolErrors, "invalid references event order or body")
			}
			references, row.References, row.ClientMetrics.ReferencesMS = true, decoded.References, &elapsed
		case "content":
			if !references || decoded.Content == nil {
				row.ProtocolErrors = append(row.ProtocolErrors, "content without references or statement")
				return
			}
			row.Content = append(row.Content, *decoded.Content)
			if row.ClientMetrics.FirstContentMS == nil {
				row.ClientMetrics.FirstContentMS = &elapsed
			}
		case "complete":
			terminal = true
			if !references || decoded.Result == nil {
				row.ProtocolErrors = append(row.ProtocolErrors, "complete without references or result")
			}
			row.Result = decoded.Result
			if row.Result != nil {
				row.Metrics, row.Usage = &row.Result.Metrics, row.Result.Usage
			}
		case "error":
			terminal = true
			row.Error = decoded.Error
			if decoded.Error == nil || decoded.Completed {
				row.ProtocolErrors = append(row.ProtocolErrors, "invalid terminal error")
			}
		default:
			row.ProtocolErrors = append(row.ProtocolErrors, "unknown SSE event: "+event)
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !utf8.ValidString(line) {
			row.ProtocolErrors = append(row.ProtocolErrors, "invalid UTF-8 SSE line")
			break
		}
		if line == "" {
			dispatch()
			event, payload = "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			payload = append(payload, value)
		}
	}
	if err := scanner.Err(); err != nil {
		row.TransportError = err.Error()
	}
	if reader.N <= 0 {
		row.ProtocolErrors = append(row.ProtocolErrors, "SSE response exceeds body limit")
	}
	if event != "" || len(payload) > 0 {
		row.TrailingData = strings.Join(payload, "\n")
		row.ProtocolErrors = append(row.ProtocolErrors, "truncated SSE frame at EOF")
	}
	if !terminal {
		row.ProtocolErrors = append(row.ProtocolErrors, "SSE ended without complete or error")
	}
	return
}

func stage4Validate(row *stage4Run, sources map[string]moerise.Note) {
	fail := func(message string) { row.HardFailures = append(row.HardFailures, message) }
	row.HardFailures = append(row.HardFailures, row.ProtocolErrors...)
	if row.HTTPStatus != http.StatusOK || row.TransportError != "" || row.Error != nil || row.Result == nil {
		fail("request did not complete successfully; inspect HTTP, error and partial stream evidence")
	}
	if row.RequestID == "" {
		fail("missing request ID")
	}
	if row.Error != nil {
		if row.Error.Code == "" || row.Error.Message == "" || row.Error.RequestID != row.RequestID {
			fail("invalid public error or request ID")
		}
		if len(row.Content) > 0 && row.Error.Retryable {
			fail("error after delivered content is retryable")
		}
	}
	refs := map[string]moerise.Evidence{}
	for i, ref := range row.References {
		label := fmt.Sprintf("reference %s (%s): ", ref.ID, ref.Name)
		note, exists := sources[ref.Name]
		if !exists {
			fail(label + "source is outside the isolated frozen corpus")
			continue
		}
		if ref.ID != "E"+strconv.Itoa(i+1) {
			fail(label + "noncanonical or duplicate citation ID")
		}
		refs[ref.ID] = ref
		if ref.SourceHash != note.SourceHash || ref.CreateTime != note.CreateTime ||
			ref.UpdateTime != note.UpdateTime || ref.URL != note.URL {
			fail(label + "source version, dates or URL differ from the imported snapshot")
		}
		if !stage4SourceAllowed(row.Request, note) {
			fail(label + "source violates request names, tags, state or creation-time bounds")
		}
		body := note.Content
		if ref.ByteStart < 0 || ref.ByteEnd <= ref.ByteStart || ref.ByteEnd > len(body) {
			fail(label + "invalid byte range")
			continue
		}
		text := body[ref.ByteStart:ref.ByteEnd]
		if !utf8.ValidString(text) || text != ref.Content {
			fail(label + "fragment does not match the UTF-8 source byte range")
		}
		first := 1 + strings.Count(body[:ref.ByteStart], "\n")
		last := first + strings.Count(strings.TrimSuffix(text, "\n"), "\n")
		if ref.LineStart != first || ref.LineEnd != last {
			fail(label + "line range does not match the source byte range")
		}
	}
	cited := map[string]bool{}
	facts := 0
	for i, statement := range row.Content {
		label := fmt.Sprintf("statement %d: ", i)
		if !slices.Contains([]string{"fact", "interpretation", "question"}, statement.Kind) ||
			strings.TrimSpace(statement.Text) == "" || !utf8.ValidString(statement.Text) {
			fail(label + "invalid statement kind or text")
		}
		if statement.Kind == "fact" {
			facts++
		}
		if statement.Kind != "question" && len(statement.Citations) == 0 {
			fail(label + "uncited factual statement or interpretation")
		}
		seen, distinct := map[string]bool{}, map[string]bool{}
		for _, id := range statement.Citations {
			ref, exists := refs[id]
			if !exists || seen[id] {
				fail(label + "unknown or duplicate citation " + id)
			}
			seen[id] = true
			if exists {
				cited[ref.Name], distinct[ref.Name] = true, true
			}
		}
		if statement.Kind == "interpretation" && (len(row.Request.Names) > 1 || row.Request.Timeline) && len(distinct) < 2 {
			fail(label + "cross-note interpretation cites fewer than two sources")
		}
	}
	if result := row.Result; result != nil {
		kind := "answer"
		if row.Request.Timeline {
			kind = "timeline"
		} else if len(row.Request.Names) > 1 {
			kind = "multi"
		} else if len(row.Request.Names) == 1 {
			kind = "single"
		}
		if !result.Completed || result.Kind != kind || !slices.Contains([]string{"answered", "insufficient"}, result.Status) {
			fail("invalid complete result kind, status or completed flag")
		}
		if !reflect.DeepEqual(result.References, row.References) || !reflect.DeepEqual(result.Statements, row.Content) {
			fail("complete result differs from streamed references or content")
		}
		if result.Status == "answered" && (facts == 0 || len(result.Missing) != 0) ||
			result.Status == "insufficient" && len(result.Missing) == 0 {
			fail("result status contradicts its facts or missing-information structure")
		}
		for _, missing := range result.Missing {
			if strings.TrimSpace(missing) == "" {
				fail("empty missing-information explanation")
			}
		}
		used := map[string]bool{}
		for _, name := range result.UsedSources {
			if !cited[name] || used[name] {
				fail("used_sources contains an uncited or duplicate source")
			}
			used[name] = true
		}
		if !reflect.DeepEqual(used, cited) {
			fail("used_sources does not equal the sources cited by statements")
		}
		for _, name := range result.Omitted {
			if _, exists := sources[name]; !exists {
				fail("omitted contains a source outside the frozen corpus")
			}
		}
		if result.Semantic.Used || result.Semantic.Reason == "" {
			fail("invalid retrieval degradation with embedding disabled")
		}
		for _, tokens := range []*int64{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens, result.Usage.CachedTokens} {
			if tokens != nil && *tokens < 0 {
				fail("negative token usage")
			}
		}
		metrics := result.Metrics
		if metrics.RetrievalMS < 0 || metrics.GenerationMS < 0 || metrics.TotalMS < metrics.RetrievalMS ||
			metrics.GenerationMS > metrics.TotalMS ||
			metrics.FirstContentMS != nil && (*metrics.FirstContentMS < 0 || *metrics.FirstContentMS > metrics.TotalMS) ||
			(len(row.Content) > 0) != (metrics.FirstContentMS != nil) {
			fail("invalid server latency metrics")
		}
		stage4ValidateTimeline(row, refs, fail)
	}
	row.HardInvariantsOK = len(row.HardFailures) == 0
}

func stage4SourceAllowed(request moerise.AIRequest, note moerise.Note) bool {
	// Single insight may add related history; multi insight is an explicit selection.
	if len(request.Names) > 1 && !slices.Contains(request.Names, note.Name) {
		return false
	}
	state := request.State
	if state == "" {
		state = "NORMAL"
	}
	if state != "ALL" && state != note.State {
		return false
	}
	for _, tag := range request.Tags {
		if !slices.Contains(note.Tags, tag) {
			return false
		}
	}
	created, err := time.Parse(time.RFC3339Nano, note.CreateTime)
	if err != nil {
		return false
	}
	if request.From != "" {
		from, err := time.Parse(time.RFC3339Nano, request.From)
		if err != nil || created.Before(from) {
			return false
		}
	}
	if request.Until != "" {
		until, err := time.Parse(time.RFC3339Nano, request.Until)
		if err != nil || !created.Before(until) {
			return false
		}
	}
	return true
}

func stage4ValidateTimeline(row *stage4Run, refs map[string]moerise.Evidence, fail func(string)) {
	result := row.Result
	if !row.Request.Timeline {
		if len(result.Timeline) != 0 {
			fail("non-timeline result contains timeline periods")
		}
		return
	}
	if len(refs) > 0 && len(result.Timeline) == 0 {
		fail("timeline evidence has no creation-time periods")
	}
	var previous time.Time
	for i, period := range result.Timeline {
		label := fmt.Sprintf("period %s: ", period.ID)
		from, fromErr := time.Parse(time.RFC3339Nano, period.From)
		until, untilErr := time.Parse(time.RFC3339Nano, period.Until)
		if period.ID != "P"+strconv.Itoa(i+1) || fromErr != nil || untilErr != nil ||
			!from.Before(until) || i > 0 && !from.Equal(previous) {
			fail(label + "invalid, unordered or discontinuous creation-time period")
		}
		if i == 0 && row.Request.From != "" && !stage4SameTime(period.From, row.Request.From) ||
			i == len(result.Timeline)-1 && row.Request.Until != "" && !stage4SameTime(period.Until, row.Request.Until) {
			fail(label + "period boundaries differ from the requested creation-time range")
		}
		previous = until
		if period.Empty != (len(period.RepresentativeNames) == 0) ||
			period.Empty && period.MissingReason == "" || period.Semantic.Used || period.Semantic.Reason == "" {
			fail(label + "invalid empty-period or retrieval metadata")
		}
		for _, name := range period.RepresentativeNames {
			found := false
			for _, ref := range refs {
				if ref.Name != name {
					continue
				}
				created, err := time.Parse(time.RFC3339Nano, ref.CreateTime)
				if err == nil && !created.Before(from) && created.Before(until) {
					found = true
				}
			}
			if !found {
				fail(label + "representative has no evidence created inside this period")
			}
		}
		for _, index := range period.StatementIndexes {
			if index < 0 || index >= len(result.Statements) {
				fail(label + "statement index is outside the result")
				continue
			}
			found := false
			for _, id := range result.Statements[index].Citations {
				if ref, exists := refs[id]; exists && slices.Contains(period.RepresentativeNames, ref.Name) {
					found = true
				}
			}
			if !found {
				fail(label + "statement index does not cite a representative")
			}
		}
	}
}
