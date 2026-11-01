package machine

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
)

// BenchmarkJournalBurst exercises the maximum normalized journal batch against SQLite.
func BenchmarkJournalBurst(b *testing.B) {
	e := newEnv(b)
	e.m.onEvent = nil
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		b.Fatal(err)
	}
	var before, after syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.probe.batch.Cursor = fmt.Sprintf("cursor-%d", i)
		e.probe.batch.Events = make([]Event, 1000)
		for j := range e.probe.batch.Events {
			e.probe.batch.Events[j] = Event{Key: fmt.Sprintf("event-%d-%d", i, j), Kind: "machine.journal.unit", Severity: "info", OccurredAt: e.clk.Now()}
		}
		e.poll(b)
	}
	b.StopTimer()
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
		b.Fatal(err)
	}
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec)*1000 + float64(r.Utime.Usec+r.Stime.Usec)/1000
	}
	b.ReportMetric((cpu(after)-cpu(before))/float64(b.N), "cpu-ms/batch")
	b.ReportMetric(float64(after.Maxrss)*1024, "peak-rss-B")
	var seq int
	var schema, path string
	if err := e.db.Reader().QueryRow(`PRAGMA database_list`).Scan(&seq, &schema, &path); err != nil {
		b.Fatal(err)
	}
	var size int64
	for _, file := range []string{path, path + "-wal"} {
		if info, err := os.Stat(file); err == nil {
			size += info.Size()
		} else if !os.IsNotExist(err) {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(size), "db-with-wal-B")
	var count int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM machine_events`).Scan(&count); err != nil {
		b.Fatal(err)
	}
	if count > 200000 {
		b.Fatalf("event capacity exceeded: %d", count)
	}
	e.clk.Advance(100 * 24 * time.Hour)
	if err := e.m.Prune(e.ctx); err != nil {
		b.Fatal(err)
	}
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM machine_events`).Scan(&count); err != nil {
		b.Fatal(err)
	}
	if count != 0 {
		b.Fatalf("events survived retention: %d", count)
	}
}
