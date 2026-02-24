package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

<<<<<<< HEAD
// telegramActiveFlag returns the path of the flag file that indicates
// a Telegram message is being processed by a tmux session.
func telegramActiveFlag(tmuxName string) string {
	return "/tmp/ccc-telegram-active-" + tmuxName
}

// readHookStdin reads stdin JSON with a timeout
=======
// HookData represents data received from Claude hook
type HookData struct {
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	Message        string `json:"message"`
	Title          string `json:"title"`
}

// configFromArgs parses --token and --chat-id flags from os.Args (e.g. "ccc hook-stop --token=X --chat-id=Y")
func configFromArgs() *Config {
	var token string
	var chatStr string
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--token=") {
			token = strings.TrimPrefix(arg, "--token=")
		} else if strings.HasPrefix(arg, "--chat-id=") {
			chatStr = strings.TrimPrefix(arg, "--chat-id=")
		}
	}
	if token == "" || chatStr == "" {
		return nil
	}
	chatID, err := strconv.ParseInt(chatStr, 10, 64)
	if err != nil {
		return nil
	}
	return &Config{BotToken: token, ChatID: chatID}
}

>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
func readHookStdin() ([]byte, error) {
	ch := make(chan []byte, 1)
	go func() {
		defer func() { recover() }()
		data, _ := io.ReadAll(os.Stdin)
		ch <- data
	}()

	select {
	case data := <-ch:
		return data, nil
	case <-time.After(2 * time.Second):
		return nil, nil
	}
}

func debugLog(format string, args ...interface{}) {
	f, err := os.OpenFile("/tmp/ccc-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

func handleStopHook() {
	defer func() { recover() }()

	config := configFromArgs()
	if config == nil {
		debugLog("configFromArgs returned nil")
		return
	}

	rawData, _ := readHookStdin()
	if len(rawData) == 0 {
		debugLog("rawData is empty")
		return
	}

	debugLog("stdin: %s", string(rawData))

	var hookData HookData
	if json.Unmarshal(rawData, &hookData) != nil {
		debugLog("failed to unmarshal hookData")
		return
	}

	debugLog("transcriptPath: %s", hookData.TranscriptPath)

	// Wait for transcript to be flushed to disk
	time.Sleep(500 * time.Millisecond)

	blocks := extractLastTurn(hookData.TranscriptPath)
	debugLog("extractLastTurn returned %d blocks", len(blocks))
	if len(blocks) == 0 {
		sendMessage(config, config.ChatID, "Done.")
		return
	}

	for _, block := range blocks {
		sendMessage(config, config.ChatID, block)
	}
}

// PermissionHookData represents PreToolUse hook input
type PermissionHookData struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

const permissionsDir = "/tmp/ccc-permissions"
const alwaysAllowFile = "/tmp/ccc-permissions/always_allow.json"

func loadAlwaysAllow() map[string]bool {
	data, err := os.ReadFile(alwaysAllowFile)
	if err != nil {
		return make(map[string]bool)
	}
	var m map[string]bool
	if json.Unmarshal(data, &m) != nil {
		return make(map[string]bool)
	}
	return m
}

func saveAlwaysAllow(m map[string]bool) {
	data, _ := json.Marshal(m)
	os.WriteFile(alwaysAllowFile, data, 0644)
}

func handlePermissionHook() {
	defer func() { recover() }()

	config := configFromArgs()
	if config == nil {
		return
	}

	rawData, _ := readHookStdin()
	if len(rawData) == 0 {
		return
	}

	var hookData PermissionHookData
	if json.Unmarshal(rawData, &hookData) != nil {
		return
	}

	// Check "always allow" list
	os.MkdirAll(permissionsDir, 0755)
	allowed := loadAlwaysAllow()
	if allowed[hookData.ToolName] {
		result := map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":    "PreToolUse",
				"permissionDecision": "allow",
			},
		}
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	// Build human-readable description
	desc := formatToolDescription(hookData.ToolName, hookData.ToolInput)
	text := fmt.Sprintf("Permission: %s\n\n%s", hookData.ToolName, desc)

	// Create IPC request file — use short ID for Telegram callback_data (64 byte limit)
	reqID := fmt.Sprintf("%d", time.Now().UnixNano()%1000000000)
	reqPath := filepath.Join(permissionsDir, reqID+".req")
	respPath := filepath.Join(permissionsDir, reqID+".resp")

	os.WriteFile(reqPath, rawData, 0644)
	defer os.Remove(reqPath)
	defer os.Remove(respPath)

	// Send Telegram message with 3 buttons
	// Telegram callback_data max 64 bytes — truncate tool name if needed
	toolNameShort := hookData.ToolName
	maxToolLen := 64 - len("perm:"+reqID+":always:")
	if len(toolNameShort) > maxToolLen {
		toolNameShort = toolNameShort[:maxToolLen]
	}
	keyboard := map[string]interface{}{
		"inline_keyboard": []interface{}{
			[]interface{}{
				map[string]interface{}{"text": "Yes", "callback_data": "perm:" + reqID + ":allow"},
				map[string]interface{}{"text": "Always", "callback_data": "perm:" + reqID + ":always:" + toolNameShort},
				map[string]interface{}{"text": "No", "callback_data": "perm:" + reqID + ":deny"},
			},
		},
	}

	_, err := sendMessageWithKeyboard(config, config.ChatID, text, keyboard)
	if err != nil {
		return
	}

	// Wait for response (up to 9 minutes)
	deadline := time.Now().Add(9 * time.Minute)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(respPath)
		if err == nil && len(data) > 0 {
			decision := strings.TrimSpace(string(data))
			result := map[string]interface{}{
				"hookSpecificOutput": map[string]interface{}{
					"hookEventName":    "PreToolUse",
					"permissionDecision": decision,
				},
			}
			json.NewEncoder(os.Stdout).Encode(result)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Timeout — deny
	result := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":          "PreToolUse",
			"permissionDecision":     "deny",
			"permissionDecisionReason": "Permission request timed out",
		},
	}
	json.NewEncoder(os.Stdout).Encode(result)
}

func formatToolDescription(toolName string, toolInput json.RawMessage) string {
	var input map[string]interface{}
	if json.Unmarshal(toolInput, &input) != nil {
		return string(toolInput)
	}

	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return fmt.Sprintf("```\n%s\n```", cmd)
		}
	case "Write":
		if fp, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Write file: %s", fp)
		}
	case "Edit":
		if fp, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Edit file: %s", fp)
		}
	}

	// Generic: show key=value pairs
	var parts []string
	for k, v := range input {
		s := fmt.Sprintf("%v", v)
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s: %s", k, s))
	}
	return strings.Join(parts, "\n")
}

func handleNotificationHook() {
	defer func() { recover() }()

	config := configFromArgs()
	if config == nil {
		return
	}

	rawData, _ := readHookStdin()
	if len(rawData) == 0 {
		return
	}

<<<<<<< HEAD
	hookData, err := parseHookData(rawData)
	if err != nil {
		return nil
=======
	var hookData HookData
	if json.Unmarshal(rawData, &hookData) != nil {
		return
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
	}

	title := hookData.Title
	message := hookData.Message
	if title == "" && message == "" {
		return
	}

<<<<<<< HEAD
	sessName, topicID := findSession(config, hookData.Cwd)
	if sessName == "" || config.GroupID == 0 || topicID == 0 {
		return nil
	}

	hookLog("stop-hook: session=%s transcript=%s", sessName, hookData.TranscriptPath)

	// Clear Telegram active flag when Claude stops
	tmuxName := "claude-" + strings.ReplaceAll(sessName, ".", "_")
	os.Remove(telegramActiveFlag(tmuxName))

	blocks := extractLastTurn(hookData.TranscriptPath)
	if len(blocks) == 0 {
		// No text blocks found, just send completion marker
		sendMessage(config, config.GroupID, topicID, fmt.Sprintf("✅ %s", sessName))
		return nil
	}

	for i, block := range blocks {
		text := block
		if i == len(blocks)-1 {
			text = fmt.Sprintf("✅ %s\n\n%s", sessName, block)
		}
		sendMessageGetID(config, config.GroupID, topicID, text)
	}

	return nil
=======
	text := fmt.Sprintf("%s\n\n%s", title, message)
	sendMessage(config, config.ChatID, strings.TrimSpace(text))
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
}

// extractLastTurn reads the JSONL transcript and extracts text blocks from
// the last assistant turn (after the last real user message).
func extractLastTurn(transcriptPath string) []string {
	if transcriptPath == "" {
		return nil
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}

<<<<<<< HEAD
	// transcriptLine handles both nested (message.content) and flat (root-level
	// content) JSONL formats. Claude Code v2.1.45+ may emit either.
=======
	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
	type transcriptLine struct {
		Type      string          `json:"type"`
		RequestID string          `json:"requestId,omitempty"`
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		Message   struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}

	type parsedEntry struct {
		ttype     string
		requestID string
		role      string
		content   json.RawMessage
	}

	var entries []parsedEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		lineNum++
		if len(line) == 0 {
			continue
		}
		var tl transcriptLine
		if json.Unmarshal(line, &tl) != nil {
			debugLog("line %d: unmarshal failed, first 200 chars: %s", lineNum, truncStr(string(line), 200))
			continue
		}
<<<<<<< HEAD
		// Use nested message fields if present, otherwise fall back to root-level fields
		role := tl.Message.Role
		content := tl.Message.Content
		if role == "" {
			role = tl.Role
		}
		if len(content) == 0 {
			content = tl.Content
		}
=======
		debugLog("line %d: type=%q requestID=%q role=%q contentLen=%d", lineNum, tl.Type, tl.RequestID, tl.Message.Role, len(tl.Message.Content))
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
		entries = append(entries, parsedEntry{
			ttype:     tl.Type,
			requestID: tl.RequestID,
			role:      role,
			content:   content,
		})
	}

	debugLog("total entries parsed: %d", len(entries))
	if len(entries) == 0 {
		return nil
	}

	// Find the last real user message (not a tool_result)
	lastUserIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.ttype != "user" && e.role != "user" {
			continue
		}
		if isToolResult(e.content) {
			continue
		}
		lastUserIdx = i
		break
	}

	// Collect text from assistant messages after the last user message.
	// Streaming dedup: same requestId may have multiple entries;
	// for each requestId, the last entry's text blocks win.
	startIdx := lastUserIdx + 1
	if lastUserIdx < 0 {
		startIdx = 0
	}

	reqTexts := make(map[string][]string)
	var orderedKeys []string
	var noIDTexts []string

	for i := startIdx; i < len(entries); i++ {
		e := entries[i]
		if e.ttype != "assistant" && e.role != "assistant" {
			continue
		}

		var blocks []contentBlock
		if json.Unmarshal(e.content, &blocks) != nil {
			continue
		}

		var entryTexts []string
		for _, b := range blocks {
			if b.Type != "text" {
				continue
			}
			text := strings.TrimSpace(b.Text)
			if text != "" && text != "(no content)" {
				entryTexts = append(entryTexts, text)
			}
		}

		if len(entryTexts) == 0 {
			continue
		}

		if e.requestID == "" {
			noIDTexts = append(noIDTexts, entryTexts...)
		} else {
			if _, seen := reqTexts[e.requestID]; !seen {
				orderedKeys = append(orderedKeys, e.requestID)
			}
			reqTexts[e.requestID] = entryTexts
		}
	}

	var texts []string
	for _, key := range orderedKeys {
		texts = append(texts, reqTexts[key]...)
	}
	texts = append(texts, noIDTexts...)

	return texts
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func isToolResult(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// installProjectHooks writes .claude/settings.local.json in workDir with Stop and Notification hooks
func installProjectHooks(workDir string, config *Config) error {
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}

<<<<<<< HEAD
	hookData, err := parseHookData(rawData)
	if err != nil {
		return nil
=======
	cccBin, _ := os.Executable()
	if cccBin == "" {
		cccBin = "ccc"
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
	}

	hookArgs := fmt.Sprintf(" --token=%s --chat-id=%d", config.BotToken, config.ChatID)

<<<<<<< HEAD
	sessName, topicID := findSession(config, hookData.Cwd)
	if sessName == "" || config.GroupID == 0 {
		return nil
	}

	// Handle AskUserQuestion - forward to Telegram with buttons
	if hookData.ToolName == "AskUserQuestion" && len(hookData.ToolInput.Questions) > 0 {
		for qIdx, q := range hookData.ToolInput.Questions {
			if q.Question == "" {
				continue
			}
			msg := fmt.Sprintf("❓ %s\n\n%s", q.Header, q.Question)

			var buttons [][]InlineKeyboardButton
			for i, opt := range q.Options {
				if opt.Label == "" {
					continue
				}
				totalQuestions := len(hookData.ToolInput.Questions)
				callbackData := fmt.Sprintf("%s:%d:%d:%d", sessName, qIdx, totalQuestions, i)
				if len(callbackData) > 64 {
					callbackData = callbackData[:64]
				}
				buttons = append(buttons, []InlineKeyboardButton{
					{Text: opt.Label, CallbackData: callbackData},
				})
			}

			if len(buttons) > 0 {
				sendMessageWithKeyboard(config, config.GroupID, topicID, msg, buttons)
			}
		}
		return nil
	}

	// OTP permission check for all other tools
	if !isOTPEnabled(config) {
		// No OTP configured, auto-allow everything
		outputPermissionDecision("allow", "OTP not configured")
		return nil
	}

	// OTP only applies when input came from Telegram (flag file exists and is recent).
	// The listener sets this flag before forwarding Telegram messages to tmux.
	// Flag auto-expires after 5 minutes to handle cases where stop hook didn't fire.
	tmuxName := "claude-" + strings.ReplaceAll(sessName, ".", "_")
	flagInfo, err := os.Stat(telegramActiveFlag(tmuxName))
	if err != nil || time.Since(flagInfo.ModTime()) > otpGrantDuration {
		return nil // no flag or expired, let Claude handle permissions normally
	}

	// Check for a valid OTP grant (approved within the last 5 minutes)
	if hasValidOTPGrant(tmuxName) {
		outputPermissionDecision("allow", "OTP grant still valid")
		return nil
	}

	// Build a human-readable description of what Claude wants to do
	toolDesc := hookData.ToolName
	var inputStr string
	switch hookData.ToolName {
	case "Bash":
		if hookData.ToolInput.Command != "" {
			inputStr = hookData.ToolInput.Command
		}
	case "Read":
		if hookData.ToolInput.FilePath != "" {
			inputStr = hookData.ToolInput.FilePath
		}
	case "Write", "Edit":
		if hookData.ToolInput.FilePath != "" {
			inputStr = hookData.ToolInput.FilePath
		}
	}
	if inputStr == "" {
		inputStr = string(hookData.ToolInputRaw)
	}
	if len(inputStr) > 500 {
		inputStr = inputStr[:500] + "..."
	}

	// Use session_id from hook data as unique identifier
	sessionID := hookData.SessionID
	if sessionID == "" {
		sessionID = sessName
	}

	// Only the first parallel hook sends the Telegram message.
	// If a request file already exists (from another parallel hook), just wait.
	alreadyRequested := false
	if info, err := os.Stat(otpRequestPrefix + sessionID); err == nil {
		alreadyRequested = time.Since(info.ModTime()) < 30*time.Second
	}

	req := &OTPPermissionRequest{
		SessionName: sessName,
		ToolName:    hookData.ToolName,
		ToolInput:   inputStr,
		Timestamp:   time.Now().Unix(),
	}
	writeOTPRequest(sessionID, req)

	if !alreadyRequested {
		msg := fmt.Sprintf("🔐 Permission request:\n\n🔧 %s\n📋 %s\n\nSend your OTP code to approve:", toolDesc, inputStr)
		sendMessage(config, config.GroupID, topicID, msg)
	}

	hookLog("otp-request: waiting for OTP response for session=%s tool=%s already=%v", sessName, hookData.ToolName, alreadyRequested)

	// Wait for OTP response from listener
	approved, err := waitForOTPResponse(sessionID, tmuxName, otpPermissionTimeout)
	if err != nil {
		hookLog("otp-request: timeout or error: %v", err)
		sendMessage(config, config.GroupID, topicID, "⏰ OTP timeout - permission denied")
		outputPermissionDecision("deny", "OTP approval timed out")
		return nil
	}

	if approved {
		hookLog("otp-request: approved for session=%s tool=%s", sessName, hookData.ToolName)
		writeOTPGrant(tmuxName)
		outputPermissionDecision("allow", "Approved via OTP")
	} else {
		hookLog("otp-request: denied for session=%s tool=%s", sessName, hookData.ToolName)
		outputPermissionDecision("deny", "Denied via OTP")
	}

	return nil
}

// outputPermissionDecision writes the PreToolUse hook response to stdout
func outputPermissionDecision(decision, reason string) {
	response := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	data, _ := json.Marshal(response)
	fmt.Println(string(data))
}

func handleNotificationHook() error {
	return nil
}

// isCccHook checks if a hook entry contains a ccc command
func isCccHook(entry interface{}) bool {
	if m, ok := entry.(map[string]interface{}); ok {
		if cmd, ok := m["command"].(string); ok {
			return strings.Contains(cmd, "ccc hook")
		}
		if hooks, ok := m["hooks"].([]interface{}); ok {
			for _, h := range hooks {
				if hm, ok := h.(map[string]interface{}); ok {
					if cmd, ok := hm["command"].(string); ok {
						if strings.Contains(cmd, "ccc hook") {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func removeCccHooks(hookArray []interface{}) []interface{} {
	var result []interface{}
	for _, entry := range hookArray {
		if !isCccHook(entry) {
			result = append(result, entry)
		}
	}
	return result
}

func installHook() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings.json: %w", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
	}

	cccHooks := map[string][]interface{}{
		"PreToolUse": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-permission",
						"type":    "command",
						"timeout": 300000,
					},
				},
				"matcher": "",
			},
		},
		"Stop": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-stop",
						"type":    "command",
					},
				},
			},
=======
	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": cccBin + " hook-permission" + hookArgs,
						},
					},
				},
			},
			"Stop": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": cccBin + " hook-stop" + hookArgs,
						},
					},
				},
			},
			"Notification": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": cccBin + " hook-notification" + hookArgs,
						},
					},
				},
			},
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
		},
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), data, 0600)
}

// removeProjectHooks removes the .claude/settings.local.json we created
func removeProjectHooks(workDir string) {
	settingsPath := filepath.Join(workDir, ".claude", "settings.local.json")
	os.Remove(settingsPath)
}
