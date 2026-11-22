package telemetry

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/time/rate"
	_ "modernc.org/sqlite"
)

type Receiver struct {
	db    *sql.DB
	limit *rate.Limiter
	now   func() time.Time
}

func OpenReceiver(path string) (*Receiver, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	receiver := &Receiver{db: db, limit: rate.NewLimiter(2, 8), now: time.Now}
	_, err = db.Exec(`PRAGMA secure_delete=ON; PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=3000;
		CREATE TABLE IF NOT EXISTS daily_totals (
		day TEXT NOT NULL, platform TEXT NOT NULL, client_version TEXT NOT NULL, core_version TEXT NOT NULL,
		system_major TEXT NOT NULL, operation TEXT NOT NULL, result TEXT NOT NULL, duration TEXT NOT NULL,
		source TEXT NOT NULL, count INTEGER NOT NULL,
		PRIMARY KEY(day,platform,client_version,core_version,system_major,operation,result,duration,source)) WITHOUT ROWID`)
	if err == nil {
		err = receiver.expire(context.Background())
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return receiver, nil
}

func (r *Receiver) Close() error { return r.db.Close() }

func (r *Receiver) expire(ctx context.Context) error {
	first := r.now().UTC().AddDate(0, 0, -29).Format(time.DateOnly)
	_, err := r.db.ExecContext(ctx, `DELETE FROM daily_totals WHERE day < ?`, first)
	return err
}

func (r *Receiver) RunExpiry(ctx context.Context) error {
	for {
		now := r.now().UTC()
		timer := time.NewTimer(now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			if err := r.expire(ctx); err != nil {
				return err
			}
		}
	}
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if req.URL.Path != "/v1/batches" || req.URL.RawQuery != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !r.limit.Allow() {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	batch, err := ReadBatch(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	if err := r.accumulate(req.Context(), batch); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Receiver) accumulate(ctx context.Context, batch Batch) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	day := r.now().UTC().Format(time.DateOnly)
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO daily_totals VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT DO UPDATE SET count=count+excluded.count`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range batch.Rows {
		if _, err := stmt.ExecContext(ctx, day, batch.Platform, batch.ClientVersion, batch.CoreVersion,
			batch.SystemMajor, row.Operation, row.Result, row.Duration, row.Source, row.Count); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Receiver) WeeklyReport(ctx context.Context, output io.Writer) error {
	last := r.now().UTC()
	rows, err := r.db.QueryContext(ctx, `SELECT platform,client_version,core_version,system_major,operation,source,duration,
		SUM(count), SUM(CASE WHEN result!='unknown' THEN count ELSE 0 END),
		SUM(CASE WHEN result='unknown' THEN count ELSE 0 END), SUM(CASE WHEN result='success' THEN count ELSE 0 END),
		SUM(CASE WHEN result='failure' THEN count ELSE 0 END), SUM(CASE WHEN result='canceled' THEN count ELSE 0 END),
		SUM(CASE WHEN result='conflict' THEN count ELSE 0 END), SUM(CASE WHEN result='timeout' THEN count ELSE 0 END),
		SUM(CASE WHEN result='over_limit' THEN count ELSE 0 END)
		FROM daily_totals WHERE day BETWEEN ? AND ? GROUP BY 1,2,3,4,5,6,7 ORDER BY 1,2,3,4,5,6,7`,
		last.AddDate(0, 0, -6).Format(time.DateOnly), last.Format(time.DateOnly))
	if err != nil {
		return err
	}
	defer rows.Close()
	writer := csv.NewWriter(output)
	if err := writer.Write([]string{"week_ending_utc", "platform", "client_version", "core_version", "system_major", "operation",
		"source", "duration", "sample_count", "known", "unknown", "success", "failure", "canceled", "conflict", "timeout", "over_limit", "success_pct"}); err != nil {
		return err
	}
	for rows.Next() {
		var dims [7]string
		var counts [9]int64
		var targets []any
		for i := range dims {
			targets = append(targets, &dims[i])
		}
		for i := range counts {
			targets = append(targets, &counts[i])
		}
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		record := append([]string{last.Format(time.DateOnly)}, dims[:]...)
		for _, count := range counts {
			record = append(record, strconv.FormatInt(count, 10))
		}
		pct := ""
		if counts[1] >= 20 {
			pct = fmt.Sprintf("%.1f", 100*float64(counts[3])/float64(counts[1]))
		}
		if err := writer.Write(append(record, pct)); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}
