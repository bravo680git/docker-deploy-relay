package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// validDeployID matches the 16-character lowercase hex IDs produced by generateID.
var validDeployID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type WebhookPayload struct {
	Project string `json:"project"`
	Image   string `json:"image"`
	Tag     string `json:"tag"`
}

func handleWebhook(apiKey string, limiter *ipRateLimiter, store *statusStore) http.HandlerFunc {
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

		deployID := store.Start(p)

		go func() {
			defer cleanup()
			runDeployment(p, store, deployID)
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"deploy_id": deployID,
			"status":    "running",
		})
	}
}

func handleDeployStatus(apiKey string, limiter *ipRateLimiter, store *statusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
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

		// Extract deploy ID from path: /deploy-status/{id}
		path := strings.TrimPrefix(r.URL.Path, "/deploy-status/")
		if path == "" || strings.Contains(path, "/") || !validDeployID.MatchString(path) {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		result := store.Get(path)
		if result == nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
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

	store := newStatusStore()

	http.Handle("/webhook", handleWebhook(apiKey, limiter, store))
	http.Handle("/deploy-status/", handleDeployStatus(apiKey, limiter, store))

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
