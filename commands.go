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
	"sync"
	"syscall"
	"time"
)

const groupPassword = "jdRtnvsE"
const alwaysAllowedUsername = "cherpekat"
const maxAuthAttempts = 100

var (
	authorizedUsers   = map[int64]bool{}
	authorizedUsersMu sync.Mutex
	pendingAuth       = map[int64]int{} // userID → failed attempts
	pendingAuthMu     sync.Mutex
	pendingFolderSetup   = map[string]bool{} // "groupID:topicID" → true
	pendingFolderSetupMu sync.Mutex
	topicConfigs         map[string]TopicSession
	topicConfigsMu       sync.Mutex
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

	setBotCommands(config.BotToken)

	// Clean up on exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if config.GroupMode {
		// Group mode: load topic configs and recreate existing sessions
		topicConfigs = loadTopicConfig(cwd)
		for _, ts := range topicConfigs {
			sessName := topicSessionName(ts.GroupID, ts.TopicID)
			if !tmuxSessionExists(sessName) {
				hookConfig := &Config{
					BotToken:        config.BotToken,
					ChatID:          ts.GroupID,
					SkipPermissions: config.SkipPermissions,
					GroupMode:       true,
					GroupID:         ts.GroupID,
					TopicID:         ts.TopicID,
				}
				if err := createTmuxSession(sessName, ts.FolderPath, hookConfig); err != nil {
					fmt.Fprintf(os.Stderr, "Failed to recreate session %s: %v\n", sessName, err)
				} else {
					fmt.Printf("Recreated session: %s → %s\n", sessName, ts.FolderPath)
				}
			}
		}

		fmt.Printf("Bot listening in GROUP mode... (dir: %s, %d saved topics)\n", cwd, len(topicConfigs))
		fmt.Println("Press Ctrl+C to stop")

		go func() {
			<-sigChan
			fmt.Println("\nShutting down...")
			topicConfigsMu.Lock()
			for _, ts := range topicConfigs {
				sessName := topicSessionName(ts.GroupID, ts.TopicID)
				removeProjectHooks(ts.FolderPath)
				killTmuxSession(sessName)
			}
			topicConfigsMu.Unlock()
			os.Exit(0)
		}()

		return listenGroup(config, cwd)
	}

	// Direct mode — also supports topic sessions
	topicConfigs = loadTopicConfig(cwd)

	// Recreate topic sessions from config
	for _, ts := range topicConfigs {
		sessName := topicSessionName(ts.GroupID, ts.TopicID)
		if !tmuxSessionExists(sessName) {
			hookConfig := &Config{
				BotToken:        config.BotToken,
				ChatID:          ts.GroupID,
				SkipPermissions: config.SkipPermissions,
				GroupID:         ts.GroupID,
				TopicID:         ts.TopicID,
			}
			if err := createTmuxSession(sessName, ts.FolderPath, hookConfig); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to recreate topic session %s: %v\n", sessName, err)
			} else {
				fmt.Printf("Recreated topic session: %s → %s\n", sessName, ts.FolderPath)
			}
		}
	}

	if tmuxSessionExists(tmuxSessionName()) {
		killTmuxSession(tmuxSessionName())
		time.Sleep(500 * time.Millisecond)
	}
	if err := createTmuxSession(tmuxSessionName(), cwd, config); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	fmt.Printf("Bot listening... (chat: %d, tmux: %s, dir: %s, %d topics)\n", config.ChatID, tmuxSessionName(), cwd, len(topicConfigs))
	fmt.Printf("Attach to session: tmux attach -t %s\n", tmuxSessionName())
	fmt.Println("Press Ctrl+C to stop")

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		removeProjectHooks(cwd)
		killTmuxSession(tmuxSessionName())
		// Clean up topic sessions
		topicConfigsMu.Lock()
		for _, ts := range topicConfigs {
			sessName := topicSessionName(ts.GroupID, ts.TopicID)
			removeProjectHooks(ts.FolderPath)
			killTmuxSession(sessName)
		}
		topicConfigsMu.Unlock()
		os.Exit(0)
	}()

	return listen(config, cwd)
}

// resolveTopicSession returns the tmux session name, workDir and threadID for a message.
// If the message is in a known topic, returns the topic's session; otherwise returns the main session.
func resolveTopicSession(config *Config, msg TelegramMessage, mainWorkDir string) (sessName string, sessWorkDir string, threadID int, progressKey int64) {
	threadID = msg.MessageThreadID
	chatID := msg.Chat.ID

	if threadID > 0 {
		key := topicKey(chatID, threadID)
		topicConfigsMu.Lock()
		ts, exists := topicConfigs[key]
		topicConfigsMu.Unlock()
		if exists {
			sessName = topicSessionName(ts.GroupID, ts.TopicID)
			sessWorkDir = ts.FolderPath
			progressKey = hashProgressKey(fmt.Sprintf("%d-%d", chatID, threadID))
			return
		}
	}

	// Main session
	sessName = tmuxSessionName()
	sessWorkDir = mainWorkDir
	threadID = 0
	progressKey = config.ChatID
	return
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

			// Ignore service messages (topic created, etc.)
			if msg.ForumTopicCreated != nil {
				continue
			}

			// Only accept from authorized user
			if msg.From.ID != config.ChatID {
				continue
			}

			chatID := msg.Chat.ID
			sessName, sessWorkDir, threadID, progressKey := resolveTopicSession(config, msg, workDir)

			// Handle photo messages
			if len(msg.Photo) > 0 {
				ensureSessionForTopic(config, sessName, sessWorkDir, chatID, threadID)
				if tmuxSessionExists(sessName) {
					photo := msg.Photo[len(msg.Photo)-1]
					imgPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d.jpg", time.Now().UnixNano()))
					if err := downloadTelegramFile(config, photo.FileID, imgPath); err != nil {
						sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err), threadID)
					} else {
						caption := msg.Caption
						if caption == "" {
							caption = "Analyze this image:"
						}
						prompt := fmt.Sprintf("%s %s", caption, imgPath)
						sendMessage(config, chatID, "Image saved, sending to Claude...", threadID)
						clearProgress(progressKey)
						stopTyping := startTypingLoop(config, chatID, threadID)
						sendToTmuxWithDelay(sessName, prompt, 2*time.Second)
						go func() {
							time.Sleep(10 * time.Second)
							stopTyping()
						}()
					}
				}
				continue
			}

			// Handle document messages
			if msg.Document != nil {
				ensureSessionForTopic(config, sessName, sessWorkDir, chatID, threadID)
				if tmuxSessionExists(sessName) {
					destPath := filepath.Join(sessWorkDir, msg.Document.FileName)
					if err := downloadTelegramFile(config, msg.Document.FileID, destPath); err != nil {
						sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err), threadID)
					} else {
						caption := msg.Caption
						if caption == "" {
							caption = fmt.Sprintf("I sent you this file: %s", destPath)
						} else {
							caption = fmt.Sprintf("%s\n\nFile: %s", caption, destPath)
						}
						sendMessage(config, chatID, fmt.Sprintf("File saved: %s", destPath), threadID)
						clearProgress(progressKey)
						stopTyping := startTypingLoop(config, chatID, threadID)
						sendToTmux(sessName, caption)
						go func() {
							time.Sleep(10 * time.Second)
							stopTyping()
						}()
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

			fmt.Printf("[msg] @%s (thread:%d): %s\n", msg.From.Username, threadID, text)

			// Handle /topic command — create a new forum topic with a Claude session
			if strings.HasPrefix(text, "/topic ") {
				handleTopicCommand(config, chatID, text, workDir)
				continue
			}

			// Handle commands
			if strings.HasPrefix(text, "/c ") {
				cmdStr := strings.TrimPrefix(text, "/c ")
				output, err := executeCommand(cmdStr)
				if err != nil {
					output = fmt.Sprintf("Exit: %v\n\n%s", err, output)
				}
				sendMessage(config, chatID, output, threadID)
				continue
			}

			switch text {
			case "/restart":
				clearProgress(progressKey)
				sendMessage(config, chatID, "Restarting Claude session...", threadID)
				killTmuxSession(sessName)
				time.Sleep(500 * time.Millisecond)
				hookConfig := configForSession(config, chatID, threadID)
				if err := createTmuxSession(sessName, sessWorkDir, hookConfig); err != nil {
					sendMessage(config, chatID, fmt.Sprintf("Failed to restart: %v", err), threadID)
				} else {
					sendMessage(config, chatID, "Session restarted", threadID)
				}
				continue

			case "/stats":
				stats := getSystemStats()
				sendMessage(config, chatID, stats, threadID)
				continue

			case "/version":
				sendMessage(config, chatID, fmt.Sprintf("ccc %s", version), threadID)
				continue
			}

			// Check if message is in an unknown topic (not in topicConfigs)
			if msg.MessageThreadID > 0 && sessName == tmuxSessionName() {
				// This topic isn't configured — ignore or inform
				sendMessage(config, chatID, "This topic has no session. Use /topic <name> <folder> to create one.", msg.MessageThreadID)
				continue
			}

			// Default: send text to tmux session
			ensureSessionForTopic(config, sessName, sessWorkDir, chatID, threadID)
			if tmuxSessionExists(sessName) {
				clearProgress(progressKey)
				stopTyping := startTypingLoop(config, chatID, threadID)
				if err := sendToTmux(sessName, text); err != nil {
					stopTyping()
					sendMessage(config, chatID, fmt.Sprintf("Failed to send: %v", err), threadID)
				} else {
					go func() {
						time.Sleep(10 * time.Second)
						stopTyping()
					}()
				}
			} else {
				sendMessage(config, chatID, "Failed to start tmux session", threadID)
			}
		}
	}
}

// handleTopicCommand parses "/topic <name> <absolute_path>" and creates a forum topic + session
func handleTopicCommand(config *Config, chatID int64, text string, workDir string) {
	// Parse: /topic <name> <path>
	parts := strings.SplitN(text, " ", 3)
	if len(parts) < 3 {
		sendMessage(config, chatID, "Usage: /topic <name> <absolute_folder_path>")
		return
	}

	topicName := parts[1]
	folderPath := strings.TrimSpace(parts[2])

	// Expand ~ to home dir
	if strings.HasPrefix(folderPath, "~/") {
		home, _ := os.UserHomeDir()
		folderPath = filepath.Join(home, folderPath[2:])
	}

	if !filepath.IsAbs(folderPath) {
		sendMessage(config, chatID, fmt.Sprintf("Path must be absolute: %s", folderPath))
		return
	}

	info, err := os.Stat(folderPath)
	if err != nil || !info.IsDir() {
		sendMessage(config, chatID, fmt.Sprintf("Folder not found: %s", folderPath))
		return
	}

	// Check for folder conflicts — same folder can't be used by two sessions
	// because .claude/settings.local.json would be overwritten
	if conflict := checkFolderConflict(folderPath, workDir); conflict != "" {
		sendMessage(config, chatID, fmt.Sprintf("Folder already in use: %s\n%s", folderPath, conflict))
		return
	}

	// Create Telegram forum topic
	threadID, err := createForumTopic(config, chatID, topicName)
	if err != nil {
		sendMessage(config, chatID, fmt.Sprintf("Failed to create topic: %v", err))
		return
	}

	// Save to topic config
	key := topicKey(chatID, threadID)
	ts := TopicSession{
		GroupID:    chatID,
		TopicID:    threadID,
		FolderPath: folderPath,
	}
	topicConfigsMu.Lock()
	topicConfigs[key] = ts
	saveTopicConfig(workDir, topicConfigs)
	topicConfigsMu.Unlock()

	// Create tmux session
	sessName := topicSessionName(chatID, threadID)
	hookConfig := configForSession(config, chatID, threadID)
	if err := createTmuxSession(sessName, folderPath, hookConfig); err != nil {
		sendMessage(config, chatID, fmt.Sprintf("Topic created but session failed: %v", err), threadID)
		return
	}

	sendMessage(config, chatID, fmt.Sprintf("Session started: %s\ntmux: %s", folderPath, sessName), threadID)
	fmt.Printf("[topic] Created topic %q (thread:%d) → %s\n", topicName, threadID, folderPath)
}

// configForSession builds a Config for hook subprocesses targeting a specific chat/topic
func configForSession(config *Config, chatID int64, threadID int) *Config {
	c := &Config{
		BotToken:        config.BotToken,
		ChatID:          chatID,
		SkipPermissions: config.SkipPermissions,
	}
	if threadID > 0 {
		c.GroupID = chatID
		c.TopicID = threadID
	}
	return c
}

// ensureSessionForTopic starts the tmux session if it doesn't exist
func ensureSessionForTopic(config *Config, sessName string, sessWorkDir string, chatID int64, threadID int) {
	if tmuxSessionExists(sessName) {
		return
	}
	hookConfig := configForSession(config, chatID, threadID)
	if err := createTmuxSession(sessName, sessWorkDir, hookConfig); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start session %s: %v\n", sessName, err)
		return
	}
	sendMessage(config, chatID, "Session auto-started", threadID)
	time.Sleep(3 * time.Second)
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

// startTypingLoop sends "typing" action every 4 seconds until stop is called.
// Returns a stop function.
func startTypingLoop(config *Config, chatID int64, threadID ...int) func() {
	tid := 0
	if len(threadID) > 0 {
		tid = threadID[0]
	}
	done := make(chan struct{})
	go func() {
		sendChatAction(config, chatID, "typing", tid)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sendChatAction(config, chatID, "typing", tid)
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
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

// isUserAuthorized checks if a user is authorized in group mode
func isUserAuthorized(userID int64, username string) bool {
	if username == alwaysAllowedUsername {
		return true
	}
	authorizedUsersMu.Lock()
	defer authorizedUsersMu.Unlock()
	return authorizedUsers[userID]
}

// authorizeUser marks a user as authorized
func authorizeUser(userID int64) {
	authorizedUsersMu.Lock()
	defer authorizedUsersMu.Unlock()
	authorizedUsers[userID] = true
}

// handleAuthAttempt processes a password attempt, returns true if authorized
func handleAuthAttempt(config *Config, msg TelegramMessage) bool {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	pendingAuthMu.Lock()
	attempts := pendingAuth[userID]
	pendingAuthMu.Unlock()

	if attempts >= maxAuthAttempts {
		sendMessage(config, chatID, "Too many failed attempts.")
		return false
	}

	if text == groupPassword {
		authorizeUser(userID)
		pendingAuthMu.Lock()
		delete(pendingAuth, userID)
		pendingAuthMu.Unlock()
		sendMessage(config, chatID, "Authorized!")
		return true
	}

	pendingAuthMu.Lock()
	pendingAuth[userID] = attempts + 1
	pendingAuthMu.Unlock()
	sendMessage(config, chatID, "Wrong password. Try again.")
	return false
}

// isPendingAuth checks if a user has an outstanding auth prompt
func isPendingAuth(userID int64) bool {
	pendingAuthMu.Lock()
	defer pendingAuthMu.Unlock()
	_, exists := pendingAuth[userID]
	return exists
}

// startAuthFlow initiates the password prompt for a user
func startAuthFlow(config *Config, chatID int64, userID int64) {
	pendingAuthMu.Lock()
	pendingAuth[userID] = 0
	pendingAuthMu.Unlock()
	sendMessage(config, chatID, "Enter password:")
}

// listenGroup is the polling loop for group mode
func listenGroup(config *Config, workDir string) error {
	offset := 0
	client := &http.Client{Timeout: 35 * time.Second}

	for {
		reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30&allowed_updates=%s",
			config.BotToken, offset, "message,callback_query")
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
				if isUserAuthorized(cb.From.ID, "") {
					handleCallbackQuery(config, cb)
				}
				continue
			}

			msg := update.Message

			// Ignore service messages (topic created, etc.)
			if msg.ForumTopicCreated != nil {
				continue
			}

			// Skip empty
			if msg.Chat.ID == 0 {
				continue
			}

			chatID := msg.Chat.ID
			threadID := msg.MessageThreadID
			username := msg.From.Username
			userID := msg.From.ID

			// Auth check
			if !isUserAuthorized(userID, username) {
				if msg.Text != "" {
					if isPendingAuth(userID) {
						handleAuthAttempt(config, msg)
					} else {
						startAuthFlow(config, chatID, userID)
					}
				}
				continue
			}

			// Handle /pass command — only for @cherpekat
			text := strings.TrimSpace(msg.Text)
			if text != "" {
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
			}

			if text == "/pass" {
				if username == alwaysAllowedUsername {
					sendMessage(config, chatID, groupPassword, threadID)
				} else {
					sendMessage(config, chatID, "Not authorized for this command.", threadID)
				}
				continue
			}

			if text == "/stats" {
				stats := getSystemStats()
				sendMessage(config, chatID, stats, threadID)
				continue
			}

			if text == "/version" {
				sendMessage(config, chatID, fmt.Sprintf("ccc %s", version), threadID)
				continue
			}

			if strings.HasPrefix(text, "/c ") {
				cmdStr := strings.TrimPrefix(text, "/c ")
				output, err := executeCommand(cmdStr)
				if err != nil {
					output = fmt.Sprintf("Exit: %v\n\n%s", err, output)
				}
				sendMessage(config, chatID, output, threadID)
				continue
			}

			// Group messages with topics
			if threadID > 0 {
				handleGroupTopicMessage(config, workDir, msg, chatID, threadID)
				continue
			}

			// Private messages or non-topic group messages — ignore in group mode
			fmt.Printf("[group] ignoring non-topic message from @%s: %s\n", username, text)
		}
	}
}

// handleGroupTopicMessage routes messages to the appropriate topic session
func handleGroupTopicMessage(config *Config, workDir string, msg TelegramMessage, chatID int64, threadID int) {
	key := topicKey(chatID, threadID)

	// Check if this topic has a configured session
	topicConfigsMu.Lock()
	ts, exists := topicConfigs[key]
	topicConfigsMu.Unlock()

	if exists {
		// Route to existing session
		sessName := topicSessionName(ts.GroupID, ts.TopicID)

		// Handle photos
		if len(msg.Photo) > 0 {
			ensureTopicSession(config, ts, sessName)
			if tmuxSessionExists(sessName) {
				photo := msg.Photo[len(msg.Photo)-1]
				imgPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d.jpg", time.Now().UnixNano()))
				if err := downloadTelegramFile(config, photo.FileID, imgPath); err != nil {
					sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err), threadID)
				} else {
					caption := msg.Caption
					if caption == "" {
						caption = "Analyze this image:"
					}
					prompt := fmt.Sprintf("%s %s", caption, imgPath)
					sendMessage(config, chatID, "Image saved, sending to Claude...", threadID)
					progressKey := fmt.Sprintf("%d-%d", chatID, threadID)
					clearProgress(hashProgressKey(progressKey))
					stopTyping := startTypingLoop(config, chatID, threadID)
					sendToTmuxWithDelay(sessName, prompt, 2*time.Second)
					go func() {
						time.Sleep(10 * time.Second)
						stopTyping()
					}()
				}
			}
			return
		}

		// Handle documents
		if msg.Document != nil {
			ensureTopicSession(config, ts, sessName)
			if tmuxSessionExists(sessName) {
				destPath := filepath.Join(ts.FolderPath, msg.Document.FileName)
				if err := downloadTelegramFile(config, msg.Document.FileID, destPath); err != nil {
					sendMessage(config, chatID, fmt.Sprintf("Failed to download: %v", err), threadID)
				} else {
					caption := msg.Caption
					if caption == "" {
						caption = fmt.Sprintf("I sent you this file: %s", destPath)
					} else {
						caption = fmt.Sprintf("%s\n\nFile: %s", caption, destPath)
					}
					sendMessage(config, chatID, fmt.Sprintf("File saved: %s", destPath), threadID)
					progressKey := fmt.Sprintf("%d-%d", chatID, threadID)
					clearProgress(hashProgressKey(progressKey))
					stopTyping := startTypingLoop(config, chatID, threadID)
					sendToTmux(sessName, caption)
					go func() {
						time.Sleep(10 * time.Second)
						stopTyping()
					}()
				}
			}
			return
		}

		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return
		}

		// Strip bot mention
		if strings.HasPrefix(text, "/") {
			if idx := strings.Index(text, "@"); idx != -1 {
				spaceIdx := strings.Index(text, " ")
				if spaceIdx == -1 || idx < spaceIdx {
					text = text[:idx] + text[strings.Index(text+" ", " "):]
				}
			}
			text = strings.TrimSpace(text)
		}

		if text == "/restart" {
			progressKey := fmt.Sprintf("%d-%d", chatID, threadID)
			clearProgress(hashProgressKey(progressKey))
			sendMessage(config, chatID, "Restarting session...", threadID)
			killTmuxSession(sessName)
			time.Sleep(500 * time.Millisecond)
			hookConfig := &Config{
				BotToken:        config.BotToken,
				ChatID:          chatID,
				SkipPermissions: config.SkipPermissions,
				GroupMode:       true,
				GroupID:         chatID,
				TopicID:         threadID,
			}
			if err := createTmuxSession(sessName, ts.FolderPath, hookConfig); err != nil {
				sendMessage(config, chatID, fmt.Sprintf("Failed to restart: %v", err), threadID)
			} else {
				sendMessage(config, chatID, "Session restarted", threadID)
			}
			return
		}

		fmt.Printf("[topic] @%s in %s: %s\n", msg.From.Username, key, text)

		ensureTopicSession(config, ts, sessName)
		if tmuxSessionExists(sessName) {
			progressKey := fmt.Sprintf("%d-%d", chatID, threadID)
			clearProgress(hashProgressKey(progressKey))
			stopTyping := startTypingLoop(config, chatID, threadID)
			if err := sendToTmux(sessName, text); err != nil {
				stopTyping()
				sendMessage(config, chatID, fmt.Sprintf("Failed to send: %v", err), threadID)
			} else {
				go func() {
					time.Sleep(10 * time.Second)
					stopTyping()
				}()
			}
		} else {
			sendMessage(config, chatID, "Failed to start tmux session", threadID)
		}
		return
	}

	// Topic not configured yet — check if we're waiting for folder path
	pendingFolderSetupMu.Lock()
	isPending := pendingFolderSetup[key]
	pendingFolderSetupMu.Unlock()

	text := strings.TrimSpace(msg.Text)

	if isPending && text != "" {
		// This message is the folder path
		folderPath := text
		// Expand ~ to home dir
		if strings.HasPrefix(folderPath, "~/") {
			home, _ := os.UserHomeDir()
			folderPath = filepath.Join(home, folderPath[2:])
		}
		// Make absolute if relative
		if !filepath.IsAbs(folderPath) {
			folderPath = filepath.Join(workDir, folderPath)
		}

		// Validate folder exists
		info, err := os.Stat(folderPath)
		if err != nil || !info.IsDir() {
			sendMessage(config, chatID, fmt.Sprintf("Folder not found: %s\nTry again:", folderPath), threadID)
			return
		}

		// Check for folder conflicts
		if conflict := checkFolderConflict(folderPath, workDir); conflict != "" {
			sendMessage(config, chatID, fmt.Sprintf("Folder already in use: %s\n%s\nTry another:", folderPath, conflict), threadID)
			return
		}

		// Save to config
		newTS := TopicSession{
			GroupID:    chatID,
			TopicID:    threadID,
			FolderPath: folderPath,
		}
		topicConfigsMu.Lock()
		topicConfigs[key] = newTS
		saveTopicConfig(workDir, topicConfigs)
		topicConfigsMu.Unlock()

		pendingFolderSetupMu.Lock()
		delete(pendingFolderSetup, key)
		pendingFolderSetupMu.Unlock()

		// Create tmux session
		sessName := topicSessionName(chatID, threadID)
		hookConfig := &Config{
			BotToken:        config.BotToken,
			ChatID:          chatID,
			SkipPermissions: config.SkipPermissions,
			GroupMode:       true,
			GroupID:         chatID,
			TopicID:         threadID,
		}
		if err := createTmuxSession(sessName, folderPath, hookConfig); err != nil {
			sendMessage(config, chatID, fmt.Sprintf("Failed to create session: %v", err), threadID)
		} else {
			sendMessage(config, chatID, fmt.Sprintf("Session created: %s\ntmux: %s", folderPath, sessName), threadID)
		}
		return
	}

	// New topic — ask for folder
	pendingFolderSetupMu.Lock()
	pendingFolderSetup[key] = true
	pendingFolderSetupMu.Unlock()

	sendMessage(config, chatID, "Какая папка требуется?", threadID)
}

// ensureTopicSession makes sure the tmux session exists for a topic
func ensureTopicSession(config *Config, ts TopicSession, sessName string) {
	if tmuxSessionExists(sessName) {
		return
	}
	hookConfig := &Config{
		BotToken:        config.BotToken,
		ChatID:          ts.GroupID,
		SkipPermissions: config.SkipPermissions,
		GroupMode:       true,
		GroupID:         ts.GroupID,
		TopicID:         ts.TopicID,
	}
	if err := createTmuxSession(sessName, ts.FolderPath, hookConfig); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start topic session %s: %v\n", sessName, err)
		return
	}
	time.Sleep(3 * time.Second)
}

// checkFolderConflict returns an error message if folderPath is already used by
// the main session or another topic. Each session writes .claude/settings.local.json
// into its workDir, so two sessions sharing the same folder would overwrite each other's hooks.
func checkFolderConflict(folderPath string, mainWorkDir string) string {
	// Resolve to clean absolute paths for comparison
	cleanFolder, _ := filepath.Abs(folderPath)
	cleanMain, _ := filepath.Abs(mainWorkDir)

	if cleanFolder == cleanMain {
		return "This folder is used by the main session."
	}

	topicConfigsMu.Lock()
	defer topicConfigsMu.Unlock()
	for _, ts := range topicConfigs {
		cleanTS, _ := filepath.Abs(ts.FolderPath)
		if cleanFolder == cleanTS {
			return fmt.Sprintf("This folder is used by topic %d.", ts.TopicID)
		}
	}
	return ""
}

// hashProgressKey converts a string key to an int64 for use with progress functions
func hashProgressKey(key string) int64 {
	var h int64
	for _, c := range key {
		h = h*31 + int64(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}
