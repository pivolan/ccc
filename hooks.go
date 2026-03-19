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
	"syscall"
	"time"
)

// HookData represents data received from Claude hook
type HookData struct {
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	Message        string `json:"message"`
	Title          string `json:"title"`
}

// configFromArgs parses --token, --chat-id, --group-id, --topic-id flags from os.Args
func configFromArgs() *Config {
	var token string
	var chatStr string
	var groupStr string
	var topicStr string
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--token=") {
			token = strings.TrimPrefix(arg, "--token=")
		} else if strings.HasPrefix(arg, "--chat-id=") {
			chatStr = strings.TrimPrefix(arg, "--chat-id=")
		} else if strings.HasPrefix(arg, "--group-id=") {
			groupStr = strings.TrimPrefix(arg, "--group-id=")
		} else if strings.HasPrefix(arg, "--topic-id=") {
			topicStr = strings.TrimPrefix(arg, "--topic-id=")
		}
	}
	if token == "" || chatStr == "" {
		return nil
	}
	chatID, err := strconv.ParseInt(chatStr, 10, 64)
	if err != nil {
		return nil
	}
	config := &Config{BotToken: token, ChatID: chatID}
	if groupStr != "" {
		groupID, err := strconv.ParseInt(groupStr, 10, 64)
		if err == nil {
			config.GroupMode = true
			config.GroupID = groupID
		}
	}
	if topicStr != "" {
		topicID, err := strconv.Atoi(topicStr)
		if err == nil {
			config.TopicID = topicID
		}
	}
	return config
}

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

// hookChatID returns the chat ID to send hook messages to
func hookChatID(config *Config) int64 {
	if config.GroupID != 0 {
		return config.GroupID
	}
	return config.ChatID
}

// hookThreadID returns the thread ID for topic-based messaging
func hookThreadID(config *Config) int {
	return config.TopicID
}

// hookProgressKey returns a unique key for progress tracking
func hookProgressKey(config *Config) int64 {
	if config.TopicID > 0 {
		key := fmt.Sprintf("%d-%d", hookChatID(config), config.TopicID)
		var h int64
		for _, c := range key {
			h = h*31 + int64(c)
		}
		if h < 0 {
			h = -h
		}
		return h
	}
	return config.ChatID
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

	chatID := hookChatID(config)
	threadID := hookThreadID(config)
	progressKey := hookProgressKey(config)

	// Delete progress message — the final result replaces it
	lk := lockProgress(progressKey)
	if state := loadProgress(progressKey); state != nil {
		if state.MessageID != 0 {
			deleteMessage(config, chatID, state.MessageID)
		}
	}
	clearProgress(progressKey)
	unlockProgress(lk)

	// Wait for transcript to be flushed to disk
	time.Sleep(500 * time.Millisecond)

	blocks := extractLastTurn(hookData.TranscriptPath)
	debugLog("extractLastTurn returned %d blocks", len(blocks))
	if len(blocks) == 0 {
		sendMessage(config, chatID, "Done.", threadID)
		return
	}

	for _, block := range blocks {
		sendMessage(config, chatID, block, threadID)
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

var permissionsDir = filepath.Join(os.Getenv("HOME"), ".ccc-permissions")
var alwaysAllowFile = filepath.Join(os.Getenv("HOME"), ".ccc-permissions", "always_allow.json")

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
	// Auto-approve everything — just output allow decision
	result := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":    "PreToolUse",
			"permissionDecision": "allow",
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

	var hookData HookData
	if json.Unmarshal(rawData, &hookData) != nil {
		return
	}

	title := hookData.Title
	message := hookData.Message
	if title == "" && message == "" {
		return
	}

	text := fmt.Sprintf("%s\n\n%s", title, message)
	sendMessage(config, hookChatID(config), strings.TrimSpace(text), hookThreadID(config))
}

// handlePreToolUseHook sends a monitoring notification (no stdout = no blocking).
// Registered always (alongside permission hook in non-YOLO mode).
func handlePreToolUseHook() {
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

	// Update status message with what Claude is about to do
	desc := formatToolProgressLine(hookData.ToolName, hookData.ToolInput)
	timeStr := time.Now().Format("15:04:05")
	var newLine string
	if desc != "" {
		newLine = fmt.Sprintf("`%s` → %s: %s", timeStr, hookData.ToolName, desc)
	} else {
		newLine = fmt.Sprintf("`%s` → %s", timeStr, hookData.ToolName)
	}

	updateProgress(config, hookChatID(config), newLine, hookData.TranscriptPath, hookThreadID(config))
}

// PostToolUse hook data — same fields as PreToolUse
type PostToolUseHookData struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
}

var progressDir = filepath.Join(os.Getenv("HOME"), ".ccc-progress")

type progressState struct {
	MessageID      int    `json:"message_id"`
	Text           string `json:"text"`
	UpdatedAt      int64  `json:"updated_at"`
	TranscriptPath string `json:"transcript_path,omitempty"`
}

func progressFilePath(chatID int64) string {
	return fmt.Sprintf("%s/status-%d.json", progressDir, chatID)
}

func progressLockPath(chatID int64) string {
	return fmt.Sprintf("%s/status-%d.lock", progressDir, chatID)
}

func lockProgress(chatID int64) *os.File {
	os.MkdirAll(progressDir, 0755)
	f, err := os.OpenFile(progressLockPath(chatID), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil
	}
	// Block until we get exclusive lock
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	return f
}

func unlockProgress(f *os.File) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

func loadProgress(chatID int64) *progressState {
	data, err := os.ReadFile(progressFilePath(chatID))
	if err != nil {
		return nil
	}
	var s progressState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

func saveProgress(chatID int64, s *progressState) {
	if err := os.MkdirAll(progressDir, 0755); err != nil {
		debugLog("saveProgress: MkdirAll %s failed: %v", progressDir, err)
		return
	}
	data, _ := json.Marshal(s)
	if err := os.WriteFile(progressFilePath(chatID), data, 0644); err != nil {
		debugLog("saveProgress: WriteFile %s failed: %v", progressFilePath(chatID), err)
	}
}

func clearProgress(chatID int64) {
	os.Remove(progressFilePath(chatID))
	os.Remove(progressLockPath(chatID))
}

// updateProgress adds a line to the progress message atomically (with file locking).
// It either edits the existing Telegram message or creates one if none exists yet.
func updateProgress(config *Config, chatID int64, newLine string, transcriptPath string, threadID ...int) {
	tid := 0
	if len(threadID) > 0 {
		tid = threadID[0]
	}

	progressKey := hookProgressKey(config)

	lk := lockProgress(progressKey)
	defer unlockProgress(lk)

	state := loadProgress(progressKey)
	if state == nil {
		state = &progressState{}
	}
	if transcriptPath != "" {
		state.TranscriptPath = transcriptPath
	}

	lines := strings.Split(state.Text, "\n")
	if state.Text == "" {
		lines = nil
	}
	lines = append(lines, newLine)
	const maxLines = 5
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	newText := strings.Join(lines, "\n")

	now := time.Now()
	sinceLastUpdate := now.Unix() - state.UpdatedAt

	if state.MessageID == 0 {
		msgID, err := sendMessageGetID(config, chatID, "⏳\n"+newText, tid)
		if err == nil {
			state.MessageID = msgID
		}
		state.UpdatedAt = now.Unix()
	} else if sinceLastUpdate >= 3 {
		editMessageText(config, chatID, state.MessageID, "⏳\n"+newText)
		state.UpdatedAt = now.Unix()
	}
	state.Text = newText
	saveProgress(progressKey, state)

	sendChatAction(config, chatID, "typing", tid)
}

func handlePostToolUseHook() {
	defer func() { recover() }()

	config := configFromArgs()
	if config == nil {
		return
	}

	rawData, _ := readHookStdin()
	if len(rawData) == 0 {
		return
	}

	var hookData PostToolUseHookData
	if json.Unmarshal(rawData, &hookData) != nil {
		return
	}

	// Build a short progress line for this tool
	desc := formatToolProgressLine(hookData.ToolName, hookData.ToolInput)
	timeStr := time.Now().Format("15:04:05")
	var newLine string
	if desc != "" {
		newLine = fmt.Sprintf("`%s` ✓ %s: %s", timeStr, hookData.ToolName, desc)
	} else {
		newLine = fmt.Sprintf("`%s` ✓ %s", timeStr, hookData.ToolName)
	}

	updateProgress(config, hookChatID(config), newLine, hookData.TranscriptPath, hookThreadID(config))
}

func formatToolProgressLine(toolName string, toolInput json.RawMessage) string {
	var input map[string]interface{}
	if json.Unmarshal(toolInput, &input) != nil {
		return ""
	}
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			// Show first line of command, truncated
			lines := strings.SplitN(cmd, "\n", 2)
			s := strings.TrimSpace(lines[0])
			if len(s) > 80 {
				s = s[:80] + "…"
			}
			return fmt.Sprintf("`%s`", s)
		}
	case "Write", "Edit", "MultiEdit":
		if fp, ok := input["file_path"].(string); ok {
			return fp
		}
	case "Read":
		if fp, ok := input["file_path"].(string); ok {
			return fp
		}
	case "Glob":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			if len(desc) > 60 {
				desc = desc[:60] + "…"
			}
			return desc
		}
	case "WebFetch", "WebSearch":
		if u, ok := input["url"].(string); ok {
			return u
		}
		if q, ok := input["query"].(string); ok {
			return q
		}
	}
	return ""
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

	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	type transcriptLine struct {
		Type      string  `json:"type"`
		RequestID string  `json:"requestId,omitempty"`
		Message   message `json:"message"`
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
		debugLog("line %d: type=%q requestID=%q role=%q contentLen=%d", lineNum, tl.Type, tl.RequestID, tl.Message.Role, len(tl.Message.Content))
		entries = append(entries, parsedEntry{
			ttype:     tl.Type,
			requestID: tl.RequestID,
			role:      tl.Message.Role,
			content:   tl.Message.Content,
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

// installProjectHooks writes .claude/settings.local.json in workDir with Stop and Notification hooks.
// When SkipPermissions (YOLO mode) is enabled, the PreToolUse permission hook is omitted
// and any leftover permission IPC files are cleaned up.
func installProjectHooks(workDir string, config *Config) error {
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}

	cccBin, _ := os.Executable()
	if cccBin == "" {
		cccBin = "ccc"
	}

	hookArgs := fmt.Sprintf(" --token=%s --chat-id=%d", config.BotToken, config.ChatID)
	if config.GroupID != 0 {
		hookArgs += fmt.Sprintf(" --group-id=%d", config.GroupID)
	}
	if config.TopicID != 0 {
		hookArgs += fmt.Sprintf(" --topic-id=%d", config.TopicID)
	}

	hooks := map[string]interface{}{
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
		"PostToolUse": []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": cccBin + " hook-posttooluse" + hookArgs,
					},
				},
			},
		},
	}

	// PreToolUse: register monitoring and permission hooks as separate matchers
	// so they each get their own stdin and don't conflict on stdout decisions
	preToolUseMatchers := []interface{}{
		map[string]interface{}{
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": cccBin + " hook-pretooluse" + hookArgs,
				},
			},
		},
	}
	if !config.SkipPermissions {
		preToolUseMatchers = append(preToolUseMatchers, map[string]interface{}{
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": cccBin + " hook-permission" + hookArgs,
				},
			},
		})
	} else {
		cleanupPermissions()
	}
	hooks["PreToolUse"] = preToolUseMatchers

	settings := map[string]interface{}{
		"hooks": hooks,
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), data, 0600)
}

// cleanupPermissions removes the permission IPC directory and always-allow file
func cleanupPermissions() {
	os.RemoveAll(permissionsDir)
}

// removeProjectHooks removes the .claude/settings.local.json we created
func removeProjectHooks(workDir string) {
	settingsPath := filepath.Join(workDir, ".claude", "settings.local.json")
	os.Remove(settingsPath)
}
