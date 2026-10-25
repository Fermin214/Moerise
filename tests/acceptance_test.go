//go:build integration

package acceptance

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	moerise "moerise/internal"
)

const memosImage = "neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f"

type harness struct {
	t                                                                        *testing.T
	binary, root, config, base, memos, container, upstream, reader, operator string
	settings                                                                 moerise.Settings
	process                                                                  *exec.Cmd
	log                                                                      *os.File
	client                                                                   *http.Client
}

func randomToken() string {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", args[0], err, output)
	}
	return strings.TrimSpace(string(output))
}

func wait(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timed out: " + label)
}

func (h *harness) request(base, path, method, token string, body any) (map[string]any, int, error) {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		input = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, base+path, input)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, response.StatusCode, err
	}
	return result, response.StatusCode, nil
}

func (h *harness) call(base, path, method, token string, body any, status int) map[string]any {
	h.t.Helper()
	result, code, err := h.request(base, path, method, token, body)
	if err != nil || code != status {
		h.t.Fatalf("%s %s: status=%d expected=%d error=%v response=%v", method, path, code, status, err, result)
	}
	return result
}

func (h *harness) start() {
	h.t.Helper()
	h.process = exec.Command(h.binary, "serve", "--config", h.config, "--host", "127.0.0.1", "--port", strings.Split(h.base, ":")[2])
	h.process.Stdout, h.process.Stderr = h.log, h.log
	if err := h.process.Start(); err != nil {
		h.t.Fatal(err)
	}
	wait(h.t, "service listener", func() bool {
		_, status, err := h.request(h.base, "/health/live", "GET", "", nil)
		return err == nil && status == 200
	})
}

func (h *harness) stop() {
	if h.process != nil {
		_ = h.process.Process.Kill()
		_ = h.process.Wait()
		h.process = nil
	}
}

func (h *harness) command(expectSuccess bool, args ...string) string {
	h.t.Helper()
	args = append(args, "--config", h.config)
	output, err := exec.Command(h.binary, args...).CombinedOutput()
	if expectSuccess && err != nil {
		h.t.Fatalf("moerise %s: %v %s", args[0], err, output)
	}
	if !expectSuccess && err == nil {
		h.t.Fatalf("moerise %s should have failed", args[0])
	}
	return string(output)
}

func (h *harness) sync(mode string) {
	h.t.Helper()
	job := h.call(h.base, "/api/ext/v1/sync", "POST", h.operator, map[string]string{"mode": mode}, 202)["job"].(map[string]any)
	id := job["id"].(string)
	wait(h.t, "sync job", func() bool {
		result, status, err := h.request(h.base, "/api/ext/v1/jobs/"+id, "GET", h.operator, nil)
		if err != nil || status != 200 {
			return false
		}
		current := result["job"].(map[string]any)
		if current["status"] != "succeeded" {
			return false
		}
		if values, ok := current["result"].(map[string]any); ok {
			if merged, ok := values["merged_into"].(string); ok {
				id = merged
				return false
			}
		}
		return true
	})
}

func (h *harness) ready() {
	h.t.Helper()
	wait(h.t, "ready", func() bool {
		_, status, err := h.request(h.base, "/health/ready", "GET", "", nil)
		return err == nil && status == 200
	})
}

func (h *harness) database(kind string) *sql.DB {
	h.t.Helper()
	directory := h.settings.StateDir
	if kind == "index" {
		directory = h.settings.IndexDir
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, kind+".sqlite3"))
	if err != nil {
		h.t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func (h *harness) projection() map[string]map[string]any {
	h.t.Helper()
	db := h.database("index")
	defer db.Close()
	rows, err := db.Query("SELECT name,payload_json FROM memos")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]map[string]any{}
	for rows.Next() {
		var name, data string
		if err := rows.Scan(&name, &data); err != nil {
			h.t.Fatal(err)
		}
		var memo map[string]any
		if err := json.Unmarshal([]byte(data), &memo); err != nil {
			h.t.Fatal(err)
		}
		result[name] = memo
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *harness) allNotes(state string) []map[string]any {
	h.t.Helper()
	result := []map[string]any{}
	token := ""
	visited := map[string]bool{}
	for page := 0; page < 1000; page++ {
		path := "/api/ext/v1/notes?state=" + state + "&page_size=2"
		if token != "" {
			path += "&page_token=" + token
		}
		response := h.call(h.base, path, "GET", h.reader, nil, 200)
		for _, memo := range response["notes"].([]any) {
			result = append(result, memo.(map[string]any))
		}
		token = response["next_page_token"].(string)
		if token == "" {
			return result
		}
		if visited[token] {
			h.t.Fatal("repeated cursor")
		}
		visited[token] = true
	}
	h.t.Fatal("pagination did not finish")
	return nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	name := "moerise-acceptance-" + randomToken()[:12]
	memosListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	memosAddress := memosListener.Addr().String()
	memosListener.Close()
	docker(t, "run", "-d", "--name", name, "--mount", "type=volume,target=/var/opt/memos", "-p", memosAddress+":5230", memosImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", name).Run() })
	h := &harness{t: t, root: root, memos: "http://" + memosAddress, container: name, client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	wait(t, "Memos", func() bool {
		_, status, err := h.request(h.memos, "/api/v1/instance/profile", "GET", "", nil)
		return err == nil && status == 200
	})
	password := randomToken()
	h.call(h.memos, "/api/v1/users", "POST", "", map[string]string{"username": "acceptance-owner", "password": password, "role": "ADMIN"}, 200)
	session := h.call(h.memos, "/api/v1/auth/signin", "POST", "", map[string]any{"passwordCredentials": map[string]string{"username": "acceptance-owner", "password": password}}, 200)
	user := session["user"].(map[string]any)["name"].(string)
	pat := h.call(h.memos, "/api/v1/"+user+"/personalAccessTokens", "POST", session["accessToken"].(string), map[string]any{"description": "Moerise acceptance", "expiresInDays": 0}, 200)["token"].(string)
	s := moerise.DefaultSettings()
	s.Memos.InstanceID, s.Memos.BaseURL, s.Memos.PublicURL = "acceptance-instance", h.memos, h.memos
	s.Memos.User, s.Memos.Token, s.Memos.PageSize = user, pat, 2
	s.StateDir, s.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	s.Sync.PollSeconds, s.Sync.FullSeconds = 60, 300
	h.reader, h.operator = randomToken(), randomToken()
	s.AccessTokens = []moerise.AccessToken{{Name: "reader", Token: h.reader, Scopes: []string{"notes:read"}}, {Name: "operator", Token: h.operator, Scopes: []string{"notes:read", "sync:read", "sync:write"}}}
	s.CursorSecret = randomToken()
	h.settings, h.upstream = s, pat
	h.config = filepath.Join(root, "config.json")
	data, _ := json.Marshal(s)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + listener.Addr().String()
	listener.Close()
	executable := "moerise"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	h.binary = filepath.Join(root, executable)
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	build := exec.Command(goTool, "build", "-o", h.binary, ".")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	h.log, err = os.Create(filepath.Join(root, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.stop()
		h.log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(root, "service.log"))
			if len(data) > 6000 {
				data = data[len(data)-6000:]
			}
			t.Logf("service diagnostics:\n%s", data)
		}
	})
	h.command(true, "check-config")
	h.start()
	h.ready()
	return h
}

func TestMemosAcceptance(t *testing.T) {
	h := newHarness(t)
	h.call(h.base, "/api/ext/v1/notes", "GET", "", nil, 401)
	h.call(h.base, "/api/ext/v1/notes", "GET", "invalid", nil, 401)
	h.call(h.base, "/api/ext/v1/sync", "GET", h.reader, nil, 403)
	h.call(h.base, "/api/ext/v1/sync", "POST", h.reader, map[string]string{"mode": "full"}, 403)
	h.call(h.base, "/api/ext/v1/sync", "POST", h.operator, map[string]string{"mode": "other"}, 422)
	t.Log("PASS authentication and read scopes")

	names := []string{}
	for i := 0; i < 7; i++ {
		memo := h.call(h.memos, "/api/v1/memos", "POST", h.upstream, map[string]string{"content": fmt.Sprintf("acceptance note %d #acceptance", i), "visibility": "PRIVATE"}, 200)
		names = append(names, memo["name"].(string))
	}
	h.sync("full")
	projection := h.projection()
	for _, name := range names {
		if projection[name] == nil {
			t.Fatal("missing synchronized note")
		}
	}
	listed := h.allNotes("NORMAL")
	seen := map[string]bool{}
	for _, memo := range listed {
		name := memo["name"].(string)
		if seen[name] || memo["creator"] != h.settings.Memos.User {
			t.Fatal("duplicate or wrong author")
		}
		seen[name] = true
	}
	if len(seen) != len(names) {
		t.Fatal("incomplete pagination")
	}
	first := h.call(h.base, "/api/ext/v1/notes?page_size=1", "GET", h.reader, nil, 200)
	cursor := first["next_page_token"].(string)
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	h.call(h.base, "/api/ext/v1/notes?state=ARCHIVED&page_token="+cursor, "GET", h.reader, nil, 422)
	h.call(h.base, "/api/ext/v1/notes?page_token="+cursor+"x", "GET", h.reader, nil, 422)
	t.Log("PASS real Memos initial sync and signed pagination")

	h.call(h.memos, "/api/v1/"+names[0]+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": "edited"}, 200)
	h.call(h.memos, "/api/v1/"+names[1]+"?updateMask=state", "PATCH", h.upstream, map[string]string{"state": "ARCHIVED"}, 200)
	h.call(h.memos, "/api/v1/"+names[2], "DELETE", h.upstream, nil, 200)
	h.call(h.memos, "/api/v1/"+names[3]+"?updateMask=content,update_time", "PATCH", h.upstream, map[string]string{"content": "backdated", "updateTime": "2018-01-01T00:00:00Z"}, 200)
	h.sync("incremental")
	projection = h.projection()
	if projection[names[0]]["content"] != "edited" || projection[names[1]]["state"] != "ARCHIVED" || projection[names[3]]["content"] == "backdated" {
		t.Fatal("incorrect incremental sync")
	}
	h.call(h.base, "/api/ext/v1/notes/"+strings.TrimPrefix(names[2], "memos/"), "GET", h.reader, nil, 404)
	h.sync("full")
	projection = h.projection()
	if projection[names[2]] != nil || projection[names[3]]["content"] != "backdated" || len(h.allNotes("ARCHIVED")) != 1 {
		t.Fatal("incorrect full repair")
	}
	t.Log("PASS edit, archive, delete, and backdated repair")

	h.call(h.memos, "/api/v1/"+names[0]+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": "live source"}, 200)
	if h.call(h.base, "/api/ext/v1/notes/"+strings.TrimPrefix(names[0], "memos/"), "GET", h.reader, nil, 200)["content"] != "live source" {
		t.Fatal("stale content returned")
	}
	otherPassword := randomToken()
	h.call(h.memos, "/api/v1/users", "POST", h.upstream, map[string]string{"username": "other-author", "password": otherPassword, "role": "USER"}, 200)
	otherSession := h.call(h.memos, "/api/v1/auth/signin", "POST", "", map[string]any{"passwordCredentials": map[string]string{"username": "other-author", "password": otherPassword}}, 200)
	otherMemo := h.call(h.memos, "/api/v1/memos", "POST", otherSession["accessToken"].(string), map[string]string{"content": "another author", "visibility": "PRIVATE"}, 200)["name"].(string)
	h.sync("full")
	if h.projection()[otherMemo] != nil {
		t.Fatal("administrator token expanded author scope")
	}
	h.call(h.base, "/api/ext/v1/notes/"+strings.TrimPrefix(otherMemo, "memos/"), "GET", h.reader, nil, 404)
	t.Log("PASS live reads and author isolation")

	docker(t, "stop", h.container)
	h.call(h.base, "/api/ext/v1/notes", "GET", h.reader, nil, 503)
	job := h.call(h.base, "/api/ext/v1/sync", "POST", h.operator, map[string]string{"mode": "full"}, 202)["job"].(map[string]any)
	id := job["id"].(string)
	wait(t, "retry persisted", func() bool {
		result, code, err := h.request(h.base, "/api/ext/v1/jobs/"+id, "GET", h.operator, nil)
		return err == nil && code == 200 && result["job"].(map[string]any)["last_error"] != nil
	})
	h.stop()
	h.start()
	persisted := h.call(h.base, "/api/ext/v1/jobs/"+id, "GET", h.operator, nil, 200)["job"].(map[string]any)
	if persisted["id"] != id || (persisted["status"] != "pending" && persisted["status"] != "running") {
		t.Fatal("task did not survive restart")
	}
	docker(t, "start", h.container)
	h.sync("full")
	h.ready()
	t.Log("PASS source outage and process restart recovery")

	db := h.database("state")
	if _, err := db.Exec("INSERT INTO product_state VALUES (?, 'acceptance', 'marker', '1', '2026-10-25')", h.settings.Memos.InstanceID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	snapshot := filepath.Join(h.root, "snapshot")
	h.command(true, "backup", "--output", snapshot)
	refused := h.command(false, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "blocked"))
	if !strings.Contains(refused, "Another Moerise writer") {
		t.Fatal("live restore was not blocked")
	}
	db = h.database("state")
	if _, err := db.Exec("UPDATE product_state SET value_json='2' WHERE namespace='acceptance'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	h.stop()
	h.command(true, "rebuild-index")
	h.start()
	h.ready()
	db = h.database("state")
	var value string
	if err := db.QueryRow("SELECT value_json FROM product_state WHERE namespace='acceptance'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if value != "2" || h.projection()[names[0]] == nil {
		t.Fatal("index rebuild changed product state")
	}
	h.call(h.base, "/api/ext/v1/notes?page_token="+cursor, "GET", h.reader, nil, 422)
	h.stop()
	h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "before-restore"))
	h.start()
	h.ready()
	db = h.database("state")
	if err := db.QueryRow("SELECT value_json FROM product_state WHERE namespace='acceptance'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if value != "1" || h.projection()[names[0]] == nil {
		t.Fatal("restore did not recover product state")
	}
	h.command(true, "healthcheck", "--host", "127.0.0.1", "--port", strconv.Itoa(mustPort(t, h.base)))
	t.Log("PASS online backup, writer lock, index rebuild, and state restore")
}

func mustPort(t *testing.T, address string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(address, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
