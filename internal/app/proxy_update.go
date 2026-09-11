package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var proxyUpdateMu sync.Mutex

func ensureProxyFreshForService(ctx context.Context, service string) error {
	service = strings.ToLower(strings.TrimSpace(service))
	if service != "vk" && service != "tiktok" {
		return fmt.Errorf("unsupported proxy update service: %s", service)
	}

	script := strings.TrimSpace(os.Getenv("PROXY_SUBSCRIPTION_UPDATE_SCRIPT"))
	if script == "" {
		script = "/home/faline/trigger_admin_bot/scripts/update_proxy_from_subscription.py"
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("proxy subscription updater unavailable: %w", err)
	}

	timeoutSec := envInt("PROXY_UPDATE_BEFORE_DOWNLOAD_TIMEOUT_SEC", 45)
	if timeoutSec < 10 {
		timeoutSec = 10
	}
	updateCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	proxyUpdateMu.Lock()
	defer proxyUpdateMu.Unlock()

	args := []string{
		script,
		"--service", service,
		"--install",
		"--probe",
		"--probe-attempts", "3",
		"--probe-timeout", "8",
	}
	cmd := exec.CommandContext(updateCtx, "/usr/bin/python3", args...)
	cmd.Dir = filepath.Dir(filepath.Dir(script))
	cmd.Env = proxyUpdateEnv(os.Environ())

	started := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(started)
	output := strings.TrimSpace(string(out))
	if err != nil {
		if updateCtx.Err() != nil {
			err = updateCtx.Err()
		}
		log.Printf("proxy update before download failed service=%s elapsed=%s err=%v output=%q", service, elapsed.Round(time.Millisecond), err, clipText(output, 1200))
		if output == "" {
			return fmt.Errorf("proxy update before download failed for %s: %w", service, err)
		}
		return fmt.Errorf("proxy update before download failed for %s: %w: %s", service, err, clipText(output, 800))
	}
	if debugTriggerLogEnabled {
		log.Printf("proxy update before download ok service=%s elapsed=%s output=%q", service, elapsed.Round(time.Millisecond), clipText(output, 600))
	}
	return nil
}

func proxyUpdateEnv(base []string) []string {
	env := append([]string{}, base...)
	uid := os.Getuid()
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(uid))
		env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	if strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS")) == "" {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "bus"))
	}
	if strings.TrimSpace(os.Getenv("HOME")) == "" {
		env = append(env, "HOME=/home/faline")
	}
	return env
}
