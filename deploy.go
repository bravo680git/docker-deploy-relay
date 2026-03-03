package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var safeProjectName = regexp.MustCompile(`^[a-zA-Z0-9_/-]+$`)

var (
	errDeployInProgress = errors.New("deployment already in progress")
	errDeployBusy       = errors.New("deployment capacity reached")
)

var (
	deployMu          sync.Mutex
	deployingProjects = map[string]bool{}

	deploySemOnce sync.Once
	deploySem     chan struct{}
)

func getDeploySemaphore() chan struct{} {
	deploySemOnce.Do(func() {
		deploySem = make(chan struct{}, envInt("MAX_CONCURRENT_DEPLOYS", maxConcurrentDP))
	})
	return deploySem
}

func tryAcquireDeploySlot() bool {
	select {
	case getDeploySemaphore() <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseDeploySlot() {
	<-getDeploySemaphore()
}

func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func resolveSafePath(root, sub string) (string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(expandTilde(root)))
	if err != nil {
		return "", fmt.Errorf("resolve PROJECT_ROOT: %v", err)
	}

	clean := filepath.Clean(sub)
	if clean == "." {
		return "", fmt.Errorf("project path is empty")
	}
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("project path must be relative")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("project path escapes root")
	}

	full, err := filepath.Abs(filepath.Join(rootAbs, clean))
	if err != nil {
		return "", fmt.Errorf("resolve project path: %v", err)
	}

	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project root")
	}
	return full, nil
}

func tryBeginDeployment(project string) (func(), error) {
	if !safeProjectName.MatchString(project) {
		return nil, fmt.Errorf("invalid project name")
	}
	if !tryAcquireDeploySlot() {
		return nil, errDeployBusy
	}

	deployMu.Lock()
	if deployingProjects[project] {
		deployMu.Unlock()
		releaseDeploySlot()
		return nil, errDeployInProgress
	}
	deployingProjects[project] = true
	deployMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			deployMu.Lock()
			delete(deployingProjects, project)
			deployMu.Unlock()
			releaseDeploySlot()
		})
	}, nil
}

func runCmd(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func runDeployment(p WebhookPayload) {
	fullImage := fmt.Sprintf("%s:%s", p.Image, p.Tag)
	log.Printf("Starting deployment for %s (%s)", p.Project, fullImage)

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("DEPLOY_TIMEOUT", defaultDeployTimeout))
	defer cancel()

	// Pull image
	pullCtx, pullCancel := context.WithTimeout(ctx, envDuration("DOCKER_PULL_TIMEOUT", defaultPullTimeout))
	out, err := runCmd(pullCtx, "", "docker", "pull", fullImage)
	pullCancel()
	if err != nil {
		log.Printf("❌ %s: pull failed: %v\n%s", p.Project, err, out)
		return
	}
	log.Printf("Pulled %s", fullImage)

	// Docker compose up
	projectDir, err := resolveSafePath(envStr("PROJECT_ROOT", defaultAppRoot), p.Project)
	if err != nil {
		log.Printf("❌ %s: invalid path: %v", p.Project, err)
		return
	}

	composeCtx, composeCancel := context.WithTimeout(ctx, envDuration("DOCKER_COMPOSE_TIMEOUT", defaultComposeTimeout))
	out, err = runCmd(composeCtx, projectDir, "docker", "compose", "up", "-d")
	composeCancel()
	if err != nil {
		log.Printf("❌ %s: compose up failed: %v\n%s", p.Project, err, out)
		return
	}
	log.Printf("Deployed %s", p.Project)

	// Cleanup Hub tag (best-effort)
	hubCtx, hubCancel := context.WithTimeout(ctx, envDuration("HUB_TIMEOUT", defaultHubOpTimeout))
	if err = deleteHubTag(hubCtx, p.Image, p.Tag, false); err != nil {
		log.Printf("⚠️ %s: deployed but tag cleanup failed: %v", p.Project, err)
	} else {
		log.Printf("✅ %s: deployed, tag %s removed", p.Project, p.Tag)
	}
	hubCancel()
}
