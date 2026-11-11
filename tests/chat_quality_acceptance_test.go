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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	moerise "moerise/internal"
)

type qualityCase struct {
	ID           string   `json:"id"`
	Names        []string `json:"names"`
	Kind         string   `json:"kind"`
	Question     string   `json:"question"`
	Followup     string   `json:"followup"`
	ReviewPoints []string `json:"review_points"`
}

type qualityDataset struct {
	Version int `json:"version"`
	Notes   []struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Content string `json:"content"`
	} `json:"notes"`
	Cases []qualityCase `json:"cases"`
}

type qualityEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type qualityTurn struct {
	Input        moerise.ChatGenerate `json:"input"`
	HTTPStatus   int                  `json:"http_status"`
	Events       []qualityEvent       `json:"events"`
	Result       moerise.ChatTurn     `json:"result"`
	Error        string               `json:"error"`
	FirstDeltaMS *float64             `json:"first_delta_ms"`
	TotalMS      float64              `json:"total_ms"`
}

type qualityResult struct {
	Case            qualityCase    `json:"case"`
	Sources         []moerise.Note `json:"sources"`
	Turns           []qualityTurn  `json:"turns"`
	Replay          qualityTurn    `json:"replay"`
	Restored        bool           `json:"restored"`
	NoAutomaticSave bool           `json:"no_automatic_save"`
	SavedOriginal   bool           `json:"saved_original"`
	ProtocolPassed  bool           `json:"protocol_passed"`
	Review          string         `json:"review"`
}

func qualityDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func qualityGenerate(c *clientHarness, chatID string, input moerise.ChatGenerate) (row qualityTurn) {
	row.Input, row.Events = input, []qualityEvent{}
	start := time.Now()
	defer func() { row.TotalMS = float64(time.Since(start).Microseconds()) / 1000 }()
	data, _ := json.Marshal(input)
	request, _ := http.NewRequest("POST", c.h.base+"/api/app/v1/chats/"+chatID+"/messages", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.h.settings.Web.PublicOrigin)
	request.Header.Set("X-CSRF-Token", c.csrf)
	request.AddCookie(c.cookie)
	response, err := c.h.client.Do(request)
	if err != nil {
		row.Error = "CLIENT_TRANSPORT"
		return
	}
	defer response.Body.Close()
	row.HTTPStatus = response.StatusCode
	if response.StatusCode != 200 {
		var body struct{ Error struct{ Code string } }
		_ = json.NewDecoder(response.Body).Decode(&body)
		row.Error = body.Error.Code
		if row.Error == "" {
			row.Error = "INVALID_HTTP_RESPONSE"
		}
		return
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 8*1024*1024))
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	event, text := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := []byte(strings.TrimPrefix(line, "data: "))
			var body struct {
				moerise.ChatEvent
				Error *struct{ Code string } `json:"error"`
			}
			if json.Unmarshal(data, &body) != nil {
				row.Error = "INVALID_SSE_JSON"
				return
			}
			row.Events = append(row.Events, qualityEvent{event, slices.Clone(data)})
			if body.Turn != nil {
				row.Result = *body.Turn
			}
			if event == "delta" {
				if row.FirstDeltaMS == nil {
					value := float64(time.Since(start).Microseconds()) / 1000
					row.FirstDeltaMS = &value
				}
				text += body.Text
			}
			if body.Error != nil {
				row.Error = body.Error.Code
			}
		}
	}
	if scanner.Err() != nil {
		row.Error = "SSE_READ_FAILED"
	}
	if row.Error == "" && (len(row.Events) < 2 || row.Events[0].Event != "message" ||
		row.Events[len(row.Events)-1].Event != "complete" || row.Result.Assistant.Status != "completed" ||
		strings.TrimSpace(row.Result.Assistant.Content) == "" || text != "" && text != row.Result.Assistant.Content) {
		row.Error = "CHAT_INCOMPLETE"
	}
	return
}

func runChatQuality(t *testing.T, llm moerise.LLMConfig, output, mode string) []qualityResult {
	t.Helper()
	data, err := os.ReadFile("testdata/chat-quality.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset qualityDataset
	if err := json.Unmarshal(data, &dataset); err != nil || dataset.Version != 2 || len(dataset.Cases) != 6 {
		t.Fatal("invalid chat quality dataset", err)
	}
	report, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("report must use a new output path", err)
	}
	defer report.Close()
	testBinary, _ := os.Executable()
	binary, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	diff, err := exec.Command("git", "diff", "--binary", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	provenance := map[string]any{"mode": mode, "started_at": time.Now().UTC().Format(time.RFC3339),
		"dataset_version": dataset.Version, "dataset_sha256": qualityDigest(data), "test_binary_sha256": qualityDigest(binary),
		"base_commit": strings.TrimSpace(string(revision)), "tracked_diff_sha256": qualityDigest(diff),
		"protocol": llm.Protocol, "model": llm.Model, "memos_image": memosImage}
	results := []qualityResult{}
	flush := func() {
		t.Helper()
		data, err := json.MarshalIndent(map[string]any{"provenance": provenance, "results": results}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := report.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := report.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := report.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		if err := report.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	flush()
	h := newHarness(t)
	h.settings.LLM, h.settings.Embedding.Enabled = llm, false
	h.settings.Memos.PageSize = 100
	c := managedClient(t, h)
	h.client.Timeout = 125 * time.Second
	for _, note := range dataset.Notes {
		h.call(h.memos, "/api/v1/memos?memoId=quality-"+note.ID, "POST", h.upstream,
			map[string]string{"content": note.Content, "visibility": "PRIVATE"}, 200)
		h.call(h.memos, "/api/v1/memos/quality-"+note.ID+"?updateMask=state,create_time", "PATCH", h.upstream,
			map[string]string{"state": note.State, "createTime": "2026-10-20T08:00:00Z"}, 200)
	}
	sourceNames := func() []string {
		t.Helper()
		query := url.Values{"pageSize": {"100"}, "filter": {"creator == '" + h.settings.Memos.User + "'"}}
		page := h.call(h.memos, "/api/v1/memos?"+query.Encode(), "GET", h.upstream, nil, 200)
		if token, _ := page["nextPageToken"].(string); token != "" {
			t.Fatal("quality fixture exceeded its source inventory page")
		}
		names := []string{}
		for _, item := range page["memos"].([]any) {
			names = append(names, item.(map[string]any)["name"].(string))
		}
		slices.Sort(names)
		return names
	}
	for index, item := range dataset.Cases {
		row := qualityResult{Case: item, Sources: []moerise.Note{}, Turns: []qualityTurn{}, Review: "pending"}
		names := make([]string, len(item.Names))
		for i, id := range item.Names {
			names[i] = "memos/quality-" + id
			value := c.call("GET", "/api/app/v1/notes/quality-"+id, nil, 200)
			data, _ := json.Marshal(value)
			var note moerise.Note
			if err := json.Unmarshal(data, &note); err != nil {
				t.Fatal(err)
			}
			row.Sources = append(row.Sources, note)
		}
		chat := acceptanceChat(t, c.call("POST", "/api/app/v1/chats",
			moerise.ChatCreate{RequestID: item.ID, Title: item.ID, Names: names}, 201))
		before := sourceNames()
		for turnIndex, question := range []string{item.Question, item.Followup} {
			kind := "question"
			if turnIndex == 0 {
				kind = item.Kind
			}
			input := moerise.ChatGenerate{RequestID: fmt.Sprintf("%s-%d", item.ID, turnIndex),
				Version: chat.Version, Kind: kind, Content: question}
			turn := qualityGenerate(c, chat.ID, input)
			row.Turns = append(row.Turns, turn)
			if turn.Error != "" {
				t.Errorf("%s turn %d: %s", item.ID, turnIndex, turn.Error)
				break
			}
			if turnIndex == 0 {
				row.Replay = qualityGenerate(c, chat.ID, input)
				if row.Replay.Error != "" || row.Replay.Result.Assistant.Content != turn.Result.Assistant.Content {
					t.Errorf("%s replay did not preserve the original result", item.ID)
				}
				if index == 0 {
					h.stop()
					h.start()
					c.call("GET", "/api/app/v1/session", nil, 200)
				}
			}
		}
		restored := acceptanceChat(t, c.call("GET", "/api/app/v1/chats/"+chat.ID, nil, 200))
		row.Restored = len(restored.Turns) == len(row.Turns)
		for i, turn := range restored.Turns {
			if i >= len(row.Turns) || !reflect.DeepEqual(turn, row.Turns[i].Result) {
				row.Restored = false
			}
		}
		row.NoAutomaticSave = slices.Equal(before, sourceNames())
		if len(row.Turns) == 2 && row.Turns[0].Error == "" && row.Turns[1].Error == "" {
			last := row.Turns[1].Result
			saved := c.call("POST", "/api/app/v1/chats/"+chat.ID+"/messages/"+last.RequestID+"/save",
				map[string]any{"version": last.Assistant.Version}, 201)
			note := h.call(h.memos, "/api/v1/"+saved["name"].(string), "GET", h.upstream, nil, 200)
			row.SavedOriginal = note["content"] == last.Assistant.Content
			row.ProtocolPassed = row.Restored && row.SavedOriginal && row.NoAutomaticSave && row.Replay.Error == "" &&
				reflect.DeepEqual(row.Replay.Result, row.Turns[0].Result)
		}
		results = append(results, row)
		flush()
		if !row.ProtocolPassed {
			t.Errorf("%s failed protocol, persistence or original-text save checks", item.ID)
		}
	}
	return results
}

func TestChatQualityEvaluationAcceptance(t *testing.T) {
	model := &chatAcceptanceModel{key: randomToken(), release: make(chan struct{}, 1)}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	config := moerise.DefaultSettings().LLM
	address, key, name := server.URL+"/v1", model.key, "chat-acceptance"
	config.Enabled, config.BaseURL, config.APIKey, config.Model = true, &address, &key, &name
	config.ContextBytes, config.MaxRetries = 128000, 0
	output := filepath.Join(t.TempDir(), "quality.json")
	rows := runChatQuality(t, config, output, "controlled")
	if len(rows) != 6 || model.calls() != 12 {
		t.Fatalf("quality evaluator did not exercise all cases or replay called the model: cases=%d calls=%d", len(rows), model.calls())
	}
	for i, row := range rows {
		for turn := range 2 {
			messages := model.messages(i*2 + turn)
			var materials []struct{ Name, Content string }
			_, raw, found := strings.Cut(messages[1].Content, "\n")
			if !found || json.Unmarshal([]byte(raw), &materials) != nil || len(materials) != len(row.Sources) {
				t.Fatal("selected materials were truncated or extra notes were recalled")
			}
			got, want := map[string]string{}, map[string]string{}
			for _, material := range materials {
				got[material.Name] = material.Content
			}
			for _, note := range row.Sources {
				want[note.Name] = note.Content
			}
			if !reflect.DeepEqual(got, want) || strings.Contains(messages[1].Content, "UNSELECTED_PRIVATE_MARKER") {
				t.Fatal("quality evaluator did not preserve exact selection", got)
			}
			if turn == 0 && row.Case.Kind == "insight" && messages[len(messages)-1].Content != moerise.DefaultInsightPrompt {
				t.Fatal("quality evaluator replaced the product's default insight Prompt")
			}
			if turn == 1 && (len(messages) != 5 || messages[3].Role != "assistant" ||
				messages[3].Content != row.Turns[0].Result.Assistant.Content ||
				messages[4].Content != row.Case.Followup) {
				t.Fatal("followup lost the successful conversation")
			}
		}
	}
	data, err := os.ReadFile(output)
	if err != nil || bytes.Contains(data, []byte(key)) || !bytes.Contains(data, []byte(`"review": "pending"`)) {
		t.Fatal("quality report leaked credentials or implied a quality verdict", err)
	}
}

func TestChatQualityLiveAcceptance(t *testing.T) {
	configPath := os.Getenv("MOERISE_CHAT_QUALITY_CONFIG")
	if configPath == "" {
		t.Skip("set MOERISE_CHAT_QUALITY_CONFIG and MOERISE_CHAT_QUALITY_OUTPUT for opt-in real-model evaluation")
	}
	config, err := moerise.LoadSettings(configPath)
	if err != nil || !config.LLM.Enabled || os.Getenv("MOERISE_CHAT_QUALITY_OUTPUT") == "" {
		t.Fatal("quality evaluation requires valid enabled LLM settings and a new report path")
	}
	runChatQuality(t, config.LLM, os.Getenv("MOERISE_CHAT_QUALITY_OUTPUT"), "live")
}
