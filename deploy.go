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
		deploySem = make(chan struct{}, envInt("RELAY_MAX_CONCURRENT_DEPLOYS", maxConcurrentDP))
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

func tryBeginDeployment(project, image string) (func(), error) {
	if !safeProjectName.MatchString(project) {
		return nil, fmt.Errorf("invalid project name")
	}
	if !tryAcquireDeploySlot() {
		return nil, errDeployBusy
	}

	key := project + ":" + image
	deployMu.Lock()
	if deployingProjects[key] {
		deployMu.Unlock()
		releaseDeploySlot()
		return nil, errDeployInProgress
	}
	deployingProjects[key] = true
	deployMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			deployMu.Lock()
			delete(deployingProjects, key)
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

func detectComposeFile(projectDir string) (string, error) {
	candidates := []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	for _, filename := range candidates {
		fullPath := filepath.Join(projectDir, filename)
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			return fullPath, nil
		}
	}
	return "", fmt.Errorf("no compose file found in %s", projectDir)
}

func runDeployment(p WebhookPayload, store *statusStore, deployID string) {
	// Safety net: ensure the deployment is never left as "running" if this
	// function returns or panics without setting a terminal status.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("❌ %s: panic in deployment [%s]: %v", p.Project, deployID, rec)
			store.FailIfRunning(deployID, fmt.Sprintf("internal panic: %v", rec))
			return
		}
		store.FailIfRunning(deployID, "deployment terminated unexpectedly")
	}()

	fullImage := fmt.Sprintf("%s:%s", p.Image, p.Tag)
	log.Printf("Starting deployment for %s (%s) [%s]", p.Project, fullImage, deployID)

	ctx, cancel := context.WithTimeout(context.Background(), envDuration("RELAY_DEPLOY_TIMEOUT", defaultDeployTimeout))
	defer cancel()

	// Pull image
	store.SetPhase(deployID, PhasePulling)
	pullCtx, pullCancel := context.WithTimeout(ctx, envDuration("RELAY_DOCKER_PULL_TIMEOUT", defaultPullTimeout))
	out, err := runCmd(pullCtx, "", "docker", "pull", fullImage)
	pullCancel()
	if err != nil {
		log.Printf("❌ %s: pull failed: %v\n%s", p.Project, err, out)
		store.Fail(deployID, fmt.Sprintf("pull failed: %v", err))
		return
	}
	log.Printf("Pulled %s", fullImage)

	// Docker compose up
	projectDir, err := resolveSafePath(envStr("RELAY_PROJECT_ROOT", defaultAppRoot), p.Project)
	if err != nil {
		log.Printf("❌ %s: invalid path: %v", p.Project, err)
		store.Fail(deployID, fmt.Sprintf("invalid path: %v", err))
		return
	}

	composeFile, err := detectComposeFile(projectDir)
	if err != nil {
		log.Printf("❌ %s: %v", p.Project, err)
		store.Fail(deployID, err.Error())
		return
	}

	composeArgs := []string{"compose", "--project-directory", projectDir, "-f", composeFile, "up", "-d"}

	store.SetPhase(deployID, PhaseComposing)
	composeCtx, composeCancel := context.WithTimeout(ctx, envDuration("RELAY_DOCKER_COMPOSE_TIMEOUT", defaultComposeTimeout))
	out, err = runCmd(composeCtx, projectDir, "docker", composeArgs...)
	composeCancel()
	if err != nil {
		log.Printf("❌ %s: compose up failed: %v\n%s", p.Project, err, out)
		store.Fail(deployID, fmt.Sprintf("compose up failed: %v", err))
		return
	}
	log.Printf("Deployed %s", p.Project)

	// Mark deployment as successful
	store.Complete(deployID)

	// Cleanup Hub tag (best-effort)
	hubCtx, hubCancel := context.WithTimeout(ctx, envDuration("RELAY_HUB_TIMEOUT", defaultHubOpTimeout))
	if err = deleteHubTag(hubCtx, p.Image, p.Tag, false); err != nil {
		log.Printf("⚠️ %s: deployed but tag cleanup failed: %v", p.Project, err)
	} else {
		log.Printf("✅ %s: deployed, tag %s removed", p.Project, p.Tag)
	}
	hubCancel()
}
