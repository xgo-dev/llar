// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"syscall"
)

func main() {
	pid, err := strconv.Atoi(os.Getenv("LLARD_PID"))
	if err != nil || pid <= 0 {
		log.Fatal("LLARD_PID is required")
	}
	buildLog := os.Getenv("LLARD_E2E_BUILD_LOG")
	if buildLog == "" {
		log.Fatal("LLARD_E2E_BUILD_LOG is required")
	}
	upstreamAddr := os.Getenv("LLARD_E2E_UPSTREAM_ADDR")
	if upstreamAddr == "" {
		log.Fatal("LLARD_E2E_UPSTREAM_ADDR is required")
	}

	mux := http.NewServeMux()
	server := &http.Server{Addr: ":18081", Handler: mux}
	mux.HandleFunc("GET /identity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(upstreamAddr); err != nil {
			log.Printf("write identity: %v", err)
		}
	})
	mux.HandleFunc("GET /builds", func(w http.ResponseWriter, _ *http.Request) {
		file, err := os.Open(buildLog)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		builds := make([]string, 0)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			builds = append(builds, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(builds); err != nil {
			log.Printf("write builds: %v", err)
		}
	})
	mux.HandleFunc("GET /shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		go func() {
			process, err := os.FindProcess(pid)
			if err == nil {
				err = process.Signal(syscall.SIGTERM)
			}
			if err != nil {
				log.Printf("stop llard: %v", err)
			}
			if err := server.Shutdown(context.Background()); err != nil {
				log.Printf("stop control server: %v", err)
			}
		}()
	})

	log.Printf("llard E2E control listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
