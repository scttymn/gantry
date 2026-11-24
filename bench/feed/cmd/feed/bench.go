//go:build bench && linux

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"syscall"
)

func init() {
	addr := os.Getenv("BENCH_ADDR")
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/usage", used)
	go func() {
		fmt.Fprintln(os.Stderr, "feed: the bench port:", http.ListenAndServe(addr, mux))
	}()
}

func used(w http.ResponseWriter, r *http.Request) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	micros := func(t syscall.Timeval) int64 { return int64(t.Sec)*1e6 + int64(t.Usec) }
	json.NewEncoder(w).Encode(map[string]int64{
		"cpu_us":         micros(ru.Utime) + micros(ru.Stime),
		"rss_peak_bytes": int64(ru.Maxrss) << 10,
	})
}
