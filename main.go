package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
)

type WebhookPayload struct {
	Project string `json:"project"`
	Image   string `json:"image"`
	Tag     string `json:"tag"`
}

func handleWebhook(apiKey string, limiter *ipRateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		if ip := clientIP(r); ip != "" && !limiter.Allow(ip) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		if !apiKeysEqual(readAPIKey(r), apiKey) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		var p WebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		if p.Project == "" || p.Image == "" || p.Tag == "" {
			http.Error(w, "Missing project, image, or tag", http.StatusBadRequest)
			return
		}

		cleanup, err := tryBeginDeployment(p.Project)
		if err != nil {
			switch {
			case errors.Is(err, errDeployInProgress):
				http.Error(w, "Deployment already in progress", http.StatusConflict)
			case errors.Is(err, errDeployBusy):
				http.Error(w, "Deploy queue is full", http.StatusTooManyRequests)
			default:
				http.Error(w, "Bad Request", http.StatusBadRequest)
			}
			return
		}

		go func() {
			defer cleanup()
			runDeployment(p)
		}()

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "Deployment started")
	}
}

func main() {
	apiKey := envStr("RELAY_WEBHOOK_API_KEY", "")
	if apiKey == "" {
		log.Fatal("RELAY_WEBHOOK_API_KEY must be set")
	}

	limiter := newIPRateLimiter(
		envFloat("RELAY_RATE_LIMIT_RPS", defaultRateLimitRPS),
		envInt("RELAY_RATE_LIMIT_BURST", defaultRateLimitBurst),
	)

	http.Handle("/webhook", handleWebhook(apiKey, limiter))

	port := envStr("RELAY_PORT", defaultPort)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	log.Printf("Server starting on port %s", port)
	log.Fatal(srv.ListenAndServe())
}
