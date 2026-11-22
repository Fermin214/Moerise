package telemetry

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiverAggregateExpiryAndWeeklyReport(t *testing.T) {
	receiver, err := OpenReceiver(filepath.Join(t.TempDir(), "daily.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	today := time.Date(2026, 4, 1, 0, 30, 0, 0, time.UTC)
	day := today
	receiver.now = func() time.Time { return day }
	batch := Batch{1, "web", "1.2.3", "2.0.0", "unknown", []Row{{"save_ack", "success", "lt1s", "user", 10}}}
	send := func() {
		t.Helper()
		data, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/batches", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		receiver.ServeHTTP(response, request)
		if response.Code != 204 {
			t.Fatal(response.Code)
		}
	}
	for _, offset := range []int{-30, -29, -7, -6, 0} {
		day = today.AddDate(0, 0, offset)
		send()
	}
	batch.Rows[0].Result, batch.Rows[0].Count = "unknown", 3
	send()
	batch.Platform, batch.ClientVersion = "android", "3.0.0"
	send()
	if err := receiver.expire(t.Context()); err != nil {
		t.Fatal(err)
	}
	var earliest string
	var rows int
	if err := receiver.db.QueryRow("SELECT min(day),count(*) FROM daily_totals").Scan(&earliest, &rows); err != nil ||
		earliest != today.AddDate(0, 0, -29).Format(time.DateOnly) || rows != 6 {
		t.Fatalf("retention: %s %d %v", earliest, rows, err)
	}
	var output bytes.Buffer
	if err := receiver.WeeklyReport(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	report, err := csv.NewReader(&output).ReadAll()
	if err != nil || len(report) != 3 || report[1][1] != "android" || report[1][9] != "0" || report[1][10] != "3" ||
		report[1][17] != "" || report[2][8] != "23" || report[2][9] != "20" || report[2][10] != "3" || report[2][17] != "100.0" {
		t.Fatalf("weekly CSV: %v %v", report, err)
	}
}

func TestBatchRejectsUnexpectedDataAndLimits(t *testing.T) {
	valid := `{"schema":1,"platform":"web","client_version":"1.0.1","core_version":"unknown","system_major":"130","rows":[{"operation":"startup","result":"success","duration":"lt1s","source":"user","count":65535}]}`
	for _, data := range []string{
		strings.Replace(valid, `"schema":1`, `"schema":2`, 1),
		strings.Replace(valid, `"platform":`, `"instance":"notes.example","platform":`, 1),
		strings.Replace(valid, `"result":`, `"result":"private","result":`, 1),
		strings.Replace(valid, `"startup"`, `"service.example"`, 1),
		strings.Replace(valid, `"success"`, `"timeout: secret"`, 1),
		strings.Replace(valid, `"user"`, `"people"`, 1),
		strings.Replace(valid, `"lt1s"`, `"1256"`, 1),
		strings.Replace(valid, `"130"`, `"130.1.2"`, 1),
		strings.Replace(valid, `"1.0.1"`, `"abcd-123-4567"`, 1),
		strings.Replace(valid, "65535", "65536", 1),
		strings.Replace(valid, "65535", "1.2", 1),
		strings.Replace(valid, "65535", "0", 1),
		strings.Replace(valid, `"rows":[`, `"rows":[`+strings.Repeat(`{"operation":"create","result":"success","duration":"lt1s","source":"user","count":1},`, 256), 1),
		valid + "{}", strings.Repeat(" ", MaxBytes) + valid,
	} {
		if _, err := ReadBatch(strings.NewReader(data)); err == nil {
			t.Fatalf("accepted invalid batch (%d bytes)", len(data))
		}
	}
}
