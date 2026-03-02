package main

import (
	"os"
	"strconv"
	"time"
)

const (
	defaultPort     = "8080"
	defaultAppRoot  = "/apps"
	maxBodySize     = 1 << 20 // 1 MB
	maxConcurrentDP = 2

	defaultDeployTimeout  = 15 * time.Minute
	defaultPullTimeout    = 10 * time.Minute
	defaultComposeTimeout = 10 * time.Minute
	defaultHubOpTimeout   = 20 * time.Second
	defaultHubHTTPTimeout = 10 * time.Second

	defaultRateLimitRPS   = 1.0
	defaultRateLimitBurst = 5

	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second

	hubLoginURL      = "https://hub.docker.com/v2/users/login/"
	hubRepoTagURLFmt = "https://hub.docker.com/v2/repositories/%s/%s/tags/%s/"
)

func envStr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envFloat(name string, fallback float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envDuration(name string, fallback time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
