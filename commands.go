package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func tmuxSessionName() string {
	cwd, _ := os.Getwd()
	name := filepath.Base(cwd)
	// tmux doesn't allow dots in session names
	name = strings.ReplaceAll(name, ".", "_")
	return "ccc-" + name
}

// waitForFirstMessage polls Telegram until a message arrives, returns the chat ID
func waitForFirstMessage(token string) (int64, error) {
	offset := 0
	for {
		resp, err := telegramGet(token, fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", token, offset))
		if err != nil {
			return 0, fmt.Errorf("failed to get updates: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		resp.Body.Close()

		var updates TelegramUpdate
		if err := json.Unmarshal(body, &updates); err != nil {
			return 0, fmt.Errorf("failed to parse response: %w", err)
		}

		if !updates.OK {
			return 0, fmt.Errorf("telegram API error: %s - check your bot token", updates.Description)
		}

		for _, update := range updates.Result {
			offset = update.UpdateID + 1
			if update.Message.Chat.ID != 0 {
				return update.Message.Chat.ID, nil
			}
		}

		time.Sleep(time.Second)
	}
}

// run creates the tmux session, starts Claude, and enters the listen loop
func run(config *Config) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create tmux session with Claude
	if tmuxSessionExists(tmuxSessionName()) {
		killTmuxSession(tmuxSessionName())
		time.Sleep(500 * time.Millisecond)
	}
	if err := createTmuxSession(tmuxSessionName(), cwd, config); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	setBotCommands(config.BotToken)
	sendMessage(config, config.ChatID, "Session started in: "+cwd)

	fmt.Printf("Bot listening... (chat: %d, tmux: %s, dir: %s)\n", config.ChatID, tmuxSessionName(), cwd)
	fmt.Printf("Attach to session: tmux attach -t %s\n", tmuxSessionName())
	fmt.Println("Press Ctrl+C to stop")

	// Clean up on exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		removeProjectHooks(cwd)
		killTmuxSession(tmuxSessionName())
		os.Exit(0)
	}()

	return listen(config, cwd)
}

// listen polls Telegram for messages and dispatches them
func listen(config *Config, workDir string) error {
	offset := 0
	client := &http.Client{Timeout: 35 * time.Second}

	for {
		reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", config.BotToken, offset)
		resp, err := telegramClientGet(client, config.BotToken, reqURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Network error: %v (retrying...)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		resp.Body.Close()

		var updates TelegramUpdate
		if err := json.Unmarshal(body, &updates); err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			time.Sleep(time.Second)
			continue
		}

		if !updates.OK {
			fmt.Fprintf(os.Stderr, "Telegram API error: %s\n", updates.Description)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates.Result {
			offset = update.UpdateID + 1

			// Handle callback queries (permission buttons)
			if update.CallbackQuery != nil {
				cb := update.CallbackQuery
				if cb.From.ID != config.ChatID {
					continue
				}
				handleCallbackQuery(config, cb)
				continue
			}

			msg := update.Message

			// Only accept from authorized user
			if msg.From.ID != config.ChatID {
				continue
			}

			chatID := msg.Chat.ID

			// Handle photo messages
			if len(msg.Photo) > 0 {
				ensureTmuxSession(config, workDir)
				if tmuxSessionExists(tmuxSessionName()) {
					photo := msg.Photo[len(msg.Photo)-1]
					imgPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d.jpg", time.Now().UnixNano()))
					if err := downloadTelegramFile(config, photo.FileID, imgPath); err != nil {
						sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err))
					} else {
						caption := msg.Caption
						if caption == "" {
							caption = "Analyze this image:"
						}
						prompt := fmt.Sprintf("%s %s", caption, imgPath)
						sendMessage(config, chatID, "Image saved, sending to Claude...")
						sendToTmuxWithDelay(tmuxSessionName(), prompt, 2*time.Second)
					}
				}
				continue
			}

			// Handle document messages
			if msg.Document != nil {
				ensureTmuxSession(config, workDir)
				if tmuxSessionExists(tmuxSessionName()) {
					destPath := filepath.Join(workDir, msg.Document.FileName)
					if err := downloadTelegramFile(config, msg.Document.FileID, destPath); err != nil {
						sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err))
					} else {
						caption := msg.Caption
						if caption == "" {
							caption = fmt.Sprintf("I sent you this file: %s", destPath)
						} else {
							caption = fmt.Sprintf("%s\n\nFile: %s", caption, destPath)
						}
						sendMessage(config, chatID, fmt.Sprintf("File saved: %s", destPath))
						sendToTmux(tmuxSessionName(), caption)
					}
				}
				continue
			}

			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}

			// Strip bot mention from commands
			if strings.HasPrefix(text, "/") {
				if idx := strings.Index(text, "@"); idx != -1 {
					spaceIdx := strings.Index(text, " ")
					if spaceIdx == -1 || idx < spaceIdx {
						text = text[:idx] + text[strings.Index(text+" ", " "):]
					}
				}
				text = strings.TrimSpace(text)
			}

			fmt.Printf("[msg] @%s: %s\n", msg.From.Username, text)

			// Handle commands
			if strings.HasPrefix(text, "/c ") {
				cmdStr := strings.TrimPrefix(text, "/c ")
				output, err := executeCommand(cmdStr)
				if err != nil {
					output = fmt.Sprintf("Exit: %v\n\n%s", err, output)
				}
				sendMessage(config, chatID, output)
				continue
			}

			switch text {
			case "/restart":
				sendMessage(config, chatID, "Restarting Claude session...")
				killTmuxSession(tmuxSessionName())
				time.Sleep(500 * time.Millisecond)
				if err := createTmuxSession(tmuxSessionName(), workDir, config); err != nil {
					sendMessage(config, chatID, fmt.Sprintf("Failed to restart: %v", err))
				} else {
					sendMessage(config, chatID, "Session restarted")
				}
				continue

			case "/stats":
				stats := getSystemStats()
				sendMessage(config, chatID, stats)
				continue

			case "/version":
				sendMessage(config, chatID, fmt.Sprintf("ccc %s", version))
				continue
			}

			// Default: send text to tmux session
			ensureTmuxSession(config, workDir)
			if tmuxSessionExists(tmuxSessionName()) {
				if err := sendToTmux(tmuxSessionName(), text); err != nil {
					sendMessage(config, chatID, fmt.Sprintf("Failed to send: %v", err))
				}
			} else {
				sendMessage(config, chatID, "Failed to start tmux session")
			}
		}
	}
}

// handleCallbackQuery processes inline keyboard button presses
func handleCallbackQuery(config *Config, cb *CallbackQuery) {
	answerCallbackQuery(config, cb.ID)

	// Format: "perm:<reqID>:<decision>" or "perm:<reqID>:always:<toolName>"
	parts := strings.SplitN(cb.Data, ":", 4)
	if len(parts) < 3 || parts[0] != "perm" {
		return
	}

	reqID := parts[1]
	decision := parts[2] // "allow", "deny", or "always"

	if decision == "always" && len(parts) == 4 {
		toolName := parts[3]
		// Save to always-allow list
		allowed := loadAlwaysAllow()
		allowed[toolName] = true
		saveAlwaysAllow(allowed)
		decision = "allow"
	}

	// Write response file for the waiting hook process
	respPath := filepath.Join("/tmp/ccc-permissions", reqID+".resp")
	os.WriteFile(respPath, []byte(decision), 0644)

	// Remove buttons from the message
	if cb.Message != nil {
		editMessageReplyMarkup(config, cb.Message.Chat.ID, cb.Message.MessageID)
	}
}

// ensureTmuxSession starts the tmux session if it doesn't exist
func ensureTmuxSession(config *Config, workDir string) {
	if tmuxSessionExists(tmuxSessionName()) {
		return
	}
	if err := createTmuxSession(tmuxSessionName(), workDir, config); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start session: %v\n", err)
		return
	}
	sendMessage(config, config.ChatID, "Session auto-started")
	time.Sleep(3 * time.Second)
}

// getSystemStats returns machine stats
func getSystemStats() string {
	var sb strings.Builder
	hostname, _ := os.Hostname()
	sb.WriteString(fmt.Sprintf("%s\n\n", hostname))

	if out, err := exec.Command("uptime").Output(); err == nil {
		sb.WriteString(fmt.Sprintf("Uptime: %s\n", strings.TrimSpace(string(out))))
	}

	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		arch := strings.TrimSpace(string(out))
		var cores string
		if c, err := exec.Command("nproc").Output(); err == nil {
			cores = strings.TrimSpace(string(c))
		} else if c, err := exec.Command("sysctl", "-n", "hw.ncpu").Output(); err == nil {
			cores = strings.TrimSpace(string(c))
		}
		sb.WriteString(fmt.Sprintf("CPU: %s cores (%s)\n", cores, arch))
	}

	if out, err := exec.Command("free", "-h").Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, l := range lines {
			if strings.HasPrefix(l, "Mem:") {
				fields := strings.Fields(l)
				if len(fields) >= 4 {
					sb.WriteString(fmt.Sprintf("RAM: %s used / %s total (available: %s)\n", fields[2], fields[1], fields[6]))
				}
				break
			}
		}
	} else {
		total, _ := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if len(total) > 0 {
			totalBytes := strings.TrimSpace(string(total))
			if tb, err := strconv.ParseUint(totalBytes, 10, 64); err == nil {
				totalGB := float64(tb) / (1024 * 1024 * 1024)
				sb.WriteString(fmt.Sprintf("RAM: %.1f GB total\n", totalGB))
			}
		}
	}

	if out, err := exec.Command("df", "-h", "/").Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 5 {
				sb.WriteString(fmt.Sprintf("Disk /: %s used / %s (%s)\n", fields[2], fields[1], fields[4]))
			}
		}
	}

	if out, err := exec.Command("tmux", "list-sessions").Output(); err == nil {
		sessions := strings.TrimSpace(string(out))
		if sessions != "" {
			count := len(strings.Split(sessions, "\n"))
			sb.WriteString(fmt.Sprintf("\nTmux sessions: %d\n", count))
			sb.WriteString(sessions)
		}
	}

	return sb.String()
}

// executeCommand executes a shell command with timeout
func executeCommand(cmdStr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	shell := "bash"
	if _, err := exec.LookPath("zsh"); err == nil {
		shell = "zsh"
	}
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", cmdStr)
	cmd.Dir, _ = os.UserHomeDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}
