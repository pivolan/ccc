package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	tmuxPath   string
	claudePath string
)

func initPaths() {
	if path, err := exec.LookPath("tmux"); err == nil {
		tmuxPath = path
	} else {
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxPath = p
				break
			}
		}
	}

	if path, err := exec.LookPath("claude"); err == nil {
		claudePath = path
	} else {
		home, _ := os.UserHomeDir()
		for _, p := range []string{home + "/.local/bin/claude", "/usr/local/bin/claude"} {
			if _, err := os.Stat(p); err == nil {
				claudePath = p
				break
			}
		}
	}
}

func tmuxSessionExists(name string) bool {
	cmd := exec.Command(tmuxPath, "has-session", "-t", name)
	return cmd.Run() == nil
}

func createTmuxSession(name string, workDir string, config *Config) error {
	if claudePath == "" {
		return fmt.Errorf("claude binary not found")
	}

	args := []string{"new-session", "-d", "-s", name, "-c", workDir}
	cmd := exec.Command(tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	exec.Command(tmuxPath, "set-option", "-t", name, "mouse", "on").Run()

	// Forward HTTPS_PROXY if set
	if proxy := os.Getenv("HTTPS_PROXY"); proxy != "" {
		exec.Command(tmuxPath, "set-environment", "-t", name, "HTTPS_PROXY", proxy).Run()
		exec.Command(tmuxPath, "set-environment", "-t", name, "https_proxy", proxy).Run()
	} else if proxy := os.Getenv("https_proxy"); proxy != "" {
		exec.Command(tmuxPath, "set-environment", "-t", name, "HTTPS_PROXY", proxy).Run()
		exec.Command(tmuxPath, "set-environment", "-t", name, "https_proxy", proxy).Run()
	}

	// Install project-level hooks
	if err := installProjectHooks(workDir, config); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to install hooks: %v\n", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Build claude launch command with proxy env inline so the process inherits it
	launchCmd := claudePath
	if config.SkipPermissions {
		launchCmd += " --dangerously-skip-permissions"
	}

	if proxy := os.Getenv("HTTPS_PROXY"); proxy == "" {
		proxy = os.Getenv("https_proxy")
		if proxy != "" {
			launchCmd = fmt.Sprintf("HTTPS_PROXY=%s https_proxy=%s %s", proxy, proxy, launchCmd)
		}
	} else {
		launchCmd = fmt.Sprintf("HTTPS_PROXY=%s https_proxy=%s %s", proxy, proxy, launchCmd)
	}

	exec.Command(tmuxPath, "send-keys", "-t", name, launchCmd, "C-m").Run()

	return nil
}

func waitForClaude(session string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command(tmuxPath, "capture-pane", "-t", session, "-p")
		out, err := cmd.Output()
		if err == nil && strings.Contains(string(out), "❯") {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for Claude to start")
}

func sendToTmux(session string, text string) error {
	baseDelay := 50 * time.Millisecond
	charDelay := time.Duration(len(text)) * 500 * time.Microsecond
	delay := baseDelay + charDelay
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	return sendToTmuxWithDelay(session, text, delay)
}

func sendToTmuxWithDelay(session string, text string, delay time.Duration) error {
	// Send the text literally (without interpreting special chars)
	cmd := exec.Command(tmuxPath, "send-keys", "-t", session, "-l", text)
	if err := cmd.Run(); err != nil {
		return err
	}

	time.Sleep(delay)

	// Press Enter to submit
	exec.Command(tmuxPath, "send-keys", "-t", session, "Enter").Run()

	return nil
}

func killTmuxSession(name string) error {
	cmd := exec.Command(tmuxPath, "kill-session", "-t", name)
	return cmd.Run()
}
