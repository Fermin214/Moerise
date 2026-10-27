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
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	moerise "moerise/internal"
)

type stage3Question struct {
	Query    string   `json:"query"`
	Expected []string `json:"expected"`
}

type stage3Dataset struct {
	Revision  string           `json:"source_revision"`
	Sources   []string         `json:"sources"`
	Questions []stage3Question `json:"questions"`
	MinHit    float64          `json:"minimum_hit_at_5"`
	MinMRR    float64          `json:"minimum_mrr_at_5"`
}

type stage3Note struct {
	Ref     string `json:"ref"`
	Name    string `json:"-"`
	Hash    string `json:"sha256"`
	Content string `json:"-"`
}

func stage3Corpus(t *testing.T, dataset stage3Dataset) []stage3Note {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	notes := []stage3Note{}
	topics := map[string]string{
		"OPERATIONS": "运维", "API-V1": "接口", "DAILY-REVIEW": "回顾", "FEISHU": "飞书",
		"NOTIFICATIONS": "通知", "EMBEDDING": "模型", "SEARCH": "检索", "PRODUCT-ARCHITECTURE": "架构",
	}
	for _, path := range dataset.Sources {
		command := exec.Command("git", "-c", "safe.directory="+root, "show", dataset.Revision+":"+path)
		command.Dir = root
		data, err := command.Output()
		if err != nil {
			t.Fatalf("read pinned corpus source %s: %v", path, err)
		}
		title, heading, body, fence := "", "", []string{}, false
		flush := func() {
			text := strings.TrimSpace(strings.Join(body, "\n"))
			if heading == "" || text == "" {
				return
			}
			ref := path + "#" + heading
			id := sha256.Sum256([]byte(ref))
			topic := topics[strings.TrimSuffix(filepath.Base(path), ".md")]
			content := "#" + topic + "\n\n" + title + " · " + heading + "\n\n" + text
			hash := sha256.Sum256([]byte(content))
			notes = append(notes, stage3Note{Ref: ref, Name: "memos/s3-" + hex.EncodeToString(id[:12]),
				Hash: hex.EncodeToString(hash[:]), Content: content})
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fence = !fence
			}
			if !fence && strings.HasPrefix(line, "# ") {
				title = strings.TrimPrefix(line, "# ")
			}
			if !fence && (strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")) {
				flush()
				heading, body = strings.TrimLeft(line, "# "), nil
			} else if heading != "" {
				body = append(body, line)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		flush()
	}
	return notes
}

type stage3Resources struct {
	RSSKiB     int64   `json:"rss_kib"`
	PeakRSSKiB int64   `json:"peak_rss_kib"`
	CPUSeconds float64 `json:"cpu_seconds"`
}

func stage3Process(t *testing.T, pid int) stage3Resources {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	result := stage3Resources{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "VmRSS:" {
			result.RSSKiB, _ = strconv.ParseInt(fields[1], 10, 64)
		} else if fields[0] == "VmHWM:" {
			result.PeakRSSKiB, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data)[strings.LastIndexByte(string(data), ')')+1:])
	ticks, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil || len(fields) < 13 {
		t.Fatal("cannot read process CPU usage")
	}
	rate, _ := strconv.ParseFloat(strings.TrimSpace(string(ticks)), 64)
	user, _ := strconv.ParseFloat(fields[11], 64)
	system, _ := strconv.ParseFloat(fields[12], 64)
	result.CPUSeconds = (user + system) / rate
	return result
}

type stage3ModelUsage struct {
	DocumentCalls int `json:"document_calls"`
	QueryCalls    int `json:"query_calls"`
	Errors        int `json:"errors"`
	MaxInFlight   int `json:"max_in_flight"`
}

type stage3SearchResult struct {
	Question int      `json:"question"`
	Mode     string   `json:"mode"`
	Refs     []string `json:"refs"`
	Rank     int      `json:"rank"`
	Latency  float64  `json:"latency_ms"`
	Error    string   `json:"error,omitempty"`
}

type stage3Summary struct {
	Count    int     `json:"queries"`
	Hit      float64 `json:"hit_at_5"`
	MRR      float64 `json:"mrr_at_5"`
	MedianMS float64 `json:"median_ms"`
	P95MS    float64 `json:"p95_ms"`
	Errors   int     `json:"errors"`
}

func stage3Summarize(rows []stage3SearchResult) stage3Summary {
	result := stage3Summary{Count: len(rows)}
	latencies := make([]float64, 0, len(rows))
	for _, row := range rows {
		latencies = append(latencies, row.Latency)
		if row.Rank > 0 {
			result.Hit++
			result.MRR += 1 / float64(row.Rank)
		}
		if row.Error != "" {
			result.Errors++
		}
	}
	slices.Sort(latencies)
	result.Hit /= float64(len(rows))
	result.MRR /= float64(len(rows))
	result.MedianMS = (latencies[(len(rows)-1)/2] + latencies[len(rows)/2]) / 2
	result.P95MS = latencies[int(math.Ceil(float64(len(rows))*.95))-1]
	return result
}

// Real provider calls are opt-in; ordinary CI keeps using isolated protocol fixtures.
func TestStage3LiveAcceptance(t *testing.T) {
	configPath, output := os.Getenv("MOERISE_STAGE3_CONFIG"), os.Getenv("MOERISE_STAGE3_OUTPUT")
	if configPath == "" {
		t.Skip("set MOERISE_STAGE3_CONFIG to run the real-provider evaluation")
	}
	if runtime.GOOS != "linux" || output == "" {
		t.Fatal("live evaluation requires Linux and MOERISE_STAGE3_OUTPUT")
	}
	config, err := moerise.LoadSettings(configPath)
	if err != nil || !config.Embedding.Enabled {
		t.Fatal("live evaluation requires a valid, enabled embedding configuration")
	}
	data, err := os.ReadFile("testdata/stage3-questions.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset stage3Dataset
	if err := json.Unmarshal(data, &dataset); err != nil || len(dataset.Questions) != 20 {
		t.Fatal("expected twenty prelabelled questions")
	}
	notes := stage3Corpus(t, dataset)
	refs, names := map[string]string{}, map[string]string{}
	for _, note := range notes {
		if refs[note.Ref] != "" {
			t.Fatal("duplicate corpus reference: " + note.Ref)
		}
		refs[note.Ref], names[note.Name] = note.Name, note.Ref
	}
	for _, question := range dataset.Questions {
		for _, ref := range question.Expected {
			if refs[ref] == "" {
				t.Fatal("missing expected source: " + ref)
			}
		}
	}
	t.Logf("pinned corpus: %d public engineering notes, twenty labelled questions", len(notes))
	target, err := url.Parse(*config.Embedding.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	target.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	direct := proxy.Director
	proxy.Director = func(request *http.Request) {
		direct(request)
		request.Host = target.Host
	}
	var mu sync.Mutex
	usage, inFlight := stage3ModelUsage{}, 0
	proxy.ModifyResponse = func(response *http.Response) error {
		mu.Lock()
		if response.StatusCode != http.StatusOK {
			usage.Errors++
		}
		mu.Unlock()
		return nil
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		var request struct {
			Parameters struct {
				Purpose string `json:"text_type"`
			} `json:"parameters"`
		}
		_ = json.Unmarshal(data, &request)
		mu.Lock()
		if request.Parameters.Purpose == "document" {
			usage.DocumentCalls++
		} else {
			usage.QueryCalls++
		}
		inFlight++
		usage.MaxInFlight = max(usage.MaxInFlight, inFlight)
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		proxy.ServeHTTP(w, r)
	}))
	defer model.Close()
	snapshotUsage := func() stage3ModelUsage { mu.Lock(); defer mu.Unlock(); return usage }
	h := newHarness(t)
	h.client.Timeout = 120 * time.Second
	h.stop()
	for _, note := range notes {
		h.call(h.memos, "/api/v1/memos?memoId="+strings.TrimPrefix(note.Name, "memos/"), "POST", h.upstream,
			map[string]string{"content": note.Content, "visibility": "PRIVATE"}, 200)
	}
	h.settings.Embedding, h.settings.Memos.PageSize = config.Embedding, 100
	address := model.URL + "/api/v1"
	h.settings.Embedding.BaseURL = &address
	h.settings.Sync.PollSeconds, h.settings.Sync.FullSeconds = 3600, 86400
	saveConfig := func() {
		t.Helper()
		data, err := json.Marshal(h.settings)
		if err != nil || os.WriteFile(h.config, data, 0600) != nil {
			t.Fatal("cannot save isolated evaluation configuration")
		}
	}
	saveConfig()
	report := map[string]any{
		"source_revision": dataset.Revision, "model": config.Embedding.Model, "dimensions": config.Embedding.Dimensions,
		"batch_size": config.Embedding.BatchSize, "max_concurrency": config.Embedding.MaxConcurrency,
		"notes": notes, "questions": dataset.Questions, "go_version": runtime.Version(), "goarch": runtime.GOARCH,
		"cpu_count": runtime.NumCPU(), "started_at": time.Now().UTC().Format(time.RFC3339),
	}
	start := time.Now()
	h.start()
	h.ready()
	active := h.vectorsReady()
	report["first_index"] = map[string]any{
		"elapsed_ms": time.Since(start).Seconds() * 1000, "chunks": active.Active.Chunks,
		"resources": stage3Process(t, h.process.Process.Pid), "model_usage": snapshotUsage(),
	}
	t.Logf("first real index ready: %d notes, %d chunks, %.2fs", len(notes), active.Active.Chunks, time.Since(start).Seconds())
	results := []stage3SearchResult{}
	byMode := map[string][]stage3SearchResult{}
	search := func(number int, mode string) stage3SearchResult {
		question := dataset.Questions[number]
		start := time.Now()
		raw, status, err := h.request(h.base, "/api/ext/v1/search", "POST", h.reader,
			moerise.SearchRequest{Query: question.Query, Mode: mode, Limit: 5})
		row := stage3SearchResult{Question: number + 1, Mode: mode, Refs: []string{}, Latency: time.Since(start).Seconds() * 1000}
		if err != nil || status != 200 {
			row.Error = fmt.Sprintf("HTTP_%d", status)
			return row
		}
		data, _ := json.Marshal(raw)
		var result moerise.SearchResponse
		if json.Unmarshal(data, &result) != nil || result.Mode != mode || mode != "keyword" && !result.Semantic.Used {
			row.Error = "INVALID_MODE_OR_FALLBACK"
			return row
		}
		for i, hit := range result.Results {
			ref := names[hit.Note.Name]
			if ref == "" {
				row.Error = "UNKNOWN_RESULT"
			}
			row.Refs = append(row.Refs, ref)
			if row.Rank == 0 && slices.Contains(question.Expected, ref) {
				row.Rank = i + 1
			}
		}
		return row
	}
	for i := range dataset.Questions {
		for _, mode := range []string{"keyword", "semantic", "hybrid"} {
			row := search(i, mode)
			results, byMode[mode] = append(results, row), append(byMode[mode], row)
			if row.Rank == 0 || row.Error != "" {
				t.Logf("question %d %s: rank=%d error=%s refs=%v", i+1, mode, row.Rank, row.Error, row.Refs)
			}
			// Keep aggregate live-source traffic below Memos' per-user request budget.
			time.Sleep(1500 * time.Millisecond)
		}
		t.Logf("evaluated question %d/20", i+1)
	}
	summaries := map[string]stage3Summary{}
	for mode, rows := range byMode {
		summaries[mode] = stage3Summarize(rows)
		t.Logf("%s quality: %+v", mode, summaries[mode])
	}
	report["results"], report["summary"] = results, summaries
	report["after_search_resources"] = stage3Process(t, h.process.Process.Pid)
	beforeDiscovery := snapshotUsage()
	discovery := []map[string]any{}
	for _, seed := range []string{"docs/OPERATIONS.md#重建索引", "docs/DAILY-REVIEW.md#选择规则"} {
		raw := h.call(h.base, "/api/ext/v1/notes/"+strings.TrimPrefix(refs[seed], "memos/")+"/related?limit=5", "GET", h.reader, nil, 200)
		related := []string{}
		for _, item := range raw["results"].([]any) {
			name := item.(map[string]any)["note"].(map[string]any)["name"].(string)
			if name == refs[seed] || names[name] == "" {
				t.Fatal("related result contains its source or an unknown note")
			}
			related = append(related, names[name])
		}
		discovery = append(discovery, map[string]any{"seed": seed, "kind": "related", "refs": related})
		time.Sleep(2 * time.Second)
		for _, mode := range []string{"continue", "angle", "random"} {
			name, path, walked := refs[seed], []string{}, []string{seed}
			for range 3 {
				raw := h.call(h.base, "/api/ext/v1/discovery/walk", "POST", h.reader,
					moerise.WalkRequest{Name: name, Mode: mode, Path: path}, 200)
				data, _ := json.Marshal(raw)
				var result moerise.WalkResponse
				if json.Unmarshal(data, &result) != nil || result.Item == nil ||
					slices.Contains(path, result.Item.Note.Name) || names[result.Item.Note.Name] == "" {
					t.Fatalf("invalid real-model walk: %v", raw)
				}
				name, path = result.Item.Note.Name, result.Path
				walked = append(walked, names[name])
				time.Sleep(2 * time.Second)
			}
			discovery = append(discovery, map[string]any{"seed": seed, "kind": mode, "refs": walked})
		}
	}
	if snapshotUsage().QueryCalls != beforeDiscovery.QueryCalls || snapshotUsage().DocumentCalls != beforeDiscovery.DocumentCalls {
		t.Fatal("related notes or walking called the model")
	}
	report["discovery"] = discovery
	t.Log("PASS real-vector related notes and all three walk modes, without extra model calls")
	curves := []map[string]any{}
	for _, concurrency := range []int{1, 2, 4} {
		rows := make([]stage3SearchResult, concurrency*4)
		for round := range 4 {
			var workers sync.WaitGroup
			for worker := range concurrency {
				workers.Go(func() { rows[round*concurrency+worker] = search((round+worker)%len(dataset.Questions), "semantic") })
			}
			workers.Wait()
			time.Sleep(time.Duration(concurrency) * 1500 * time.Millisecond)
		}
		curves = append(curves, map[string]any{
			"in_flight_queries": concurrency, "summary": stage3Summarize(rows),
			"resources": stage3Process(t, h.process.Process.Pid),
		})
	}
	report["query_concurrency"] = curves
	h.stop()
	h.command(true, "rebuild-index")
	h.settings.Embedding.BatchSize = 10
	saveConfig()
	beforeRebuild := snapshotUsage()
	start = time.Now()
	h.start()
	h.ready()
	rebuilt := h.vectorsReady()
	afterRebuild := snapshotUsage()
	report["batch_10_first_index"] = map[string]any{
		"elapsed_ms": time.Since(start).Seconds() * 1000, "chunks": rebuilt.Active.Chunks,
		"resources":      stage3Process(t, h.process.Process.Pid),
		"document_calls": afterRebuild.DocumentCalls - beforeRebuild.DocumentCalls,
	}
	report["model_usage"] = snapshotUsage()
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(report)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("cannot write evaluation report")
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		value := summaries[mode]
		t.Logf("%s: Hit@5=%.2f MRR@5=%.3f median=%.1fms P95=%.1fms", mode, value.Hit, value.MRR, value.MedianMS, value.P95MS)
		if value.Errors != 0 || value.Hit < dataset.MinHit || value.MRR < dataset.MinMRR {
			t.Errorf("%s retrieval quality below the dataset acceptance threshold", mode)
		}
	}
	for _, curve := range curves {
		if curve["summary"].(stage3Summary).Errors != 0 {
			t.Error("concurrent live search returned an error")
		}
	}
	if snapshotUsage().MaxInFlight > config.Embedding.MaxConcurrency || snapshotUsage().Errors != 0 {
		t.Error("real provider calls exceeded concurrency or returned errors")
	}
	t.Log("Saved real-provider report without credentials or note contents: " + output)
}
