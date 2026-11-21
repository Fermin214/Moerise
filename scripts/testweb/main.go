package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"moerise/web"
)

func main() {
	core := flag.String("core", "", "Mock API origin")
	dist := flag.String("dist", "web/dist", "Web directory")
	address := flag.String("address", "127.0.0.1:0", "Listen address")
	flag.Parse()
	target, err := url.Parse(*core)
	if err != nil {
		panic(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "CORE_UNAVAILABLE", "retryable": true}})
	}
	fmt.Fprintln(os.Stdout, listener.Addr().(*net.TCPAddr).Port)
	server := &http.Server{Handler: web.Handler(*dist, proxy), ReadHeaderTimeout: 5 * time.Second}
	if err := server.Serve(listener); err != nil {
		panic(err)
	}
}
