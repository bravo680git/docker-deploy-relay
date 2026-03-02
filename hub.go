package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

var (
	cachedToken string
	tokenMu     sync.RWMutex
	hubHTTPOnce sync.Once
	hubHTTP     *http.Client
)

func hubClient() *http.Client {
	hubHTTPOnce.Do(func() {
		hubHTTP = &http.Client{Timeout: envDuration("HUB_HTTP_TIMEOUT", defaultHubHTTPTimeout)}
	})
	return hubHTTP
}

func getHubJWT(ctx context.Context) (string, error) {
	user, pass := os.Getenv("DOCKER_HUB_USER"), os.Getenv("DOCKER_HUB_PASS")
	if user == "" || pass == "" {
		return "", fmt.Errorf("DOCKER_HUB_USER and DOCKER_HUB_PASS must be set")
	}

	body, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubLoginURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hubClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed: %s", b)
	}

	var auth struct {
		Token string `json:"token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return "", err
	}
	return auth.Token, nil
}

func getToken(ctx context.Context, forceRefresh bool) (string, error) {
	if !forceRefresh {
		tokenMu.RLock()
		t := cachedToken
		tokenMu.RUnlock()
		if t != "" {
			return t, nil
		}
	}

	t, err := getHubJWT(ctx)
	if err != nil {
		return "", err
	}

	tokenMu.Lock()
	cachedToken = t
	tokenMu.Unlock()
	return t, nil
}

func deleteHubTag(ctx context.Context, image, tag string, retried bool) error {
	tok, err := getToken(ctx, false)
	if err != nil {
		return err
	}

	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("image must be namespace/repository: %s", image)
	}

	url := fmt.Sprintf(hubRepoTagURLFmt, parts[0], parts[1], tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "JWT "+tok)

	resp, err := hubClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && !retried {
		tokenMu.Lock()
		cachedToken = ""
		tokenMu.Unlock()
		return deleteHubTag(ctx, image, tag, true)
	}

	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete tag failed: status %d, body %s", resp.StatusCode, b)
	}
	return nil
}
