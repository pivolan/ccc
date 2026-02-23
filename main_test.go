package main

import (
	"encoding/json"
	"testing"
)

// TestExecuteCommand tests the executeCommand function
func TestExecuteCommand(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantContain string
		wantErr     bool
	}{
		{"echo", "echo hello", "hello", false},
		{"pwd", "pwd", "/", false},
		{"invalid command", "nonexistentcommand123", "", true},
		{"exit code", "exit 1", "", true},
		{"stderr output", "echo error >&2", "error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := executeCommand(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("executeCommand(%q) error = %v, wantErr %v", tt.cmd, err, tt.wantErr)
			}
			if tt.wantContain != "" && !contains(output, tt.wantContain) {
				t.Errorf("executeCommand(%q) output = %q, want to contain %q", tt.cmd, output, tt.wantContain)
			}
		})
	}
}

// TestConfigJSON tests JSON marshaling of Config
func TestConfigJSON(t *testing.T) {
	config := &Config{
		BotToken: "token123",
		ChatID:   12345,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.BotToken != config.BotToken {
		t.Errorf("BotToken mismatch")
	}
	if loaded.ChatID != config.ChatID {
		t.Errorf("ChatID mismatch")
	}
}

// TestTelegramMessageJSON tests TelegramMessage JSON parsing
func TestTelegramMessageJSON(t *testing.T) {
	jsonStr := `{
		"message_id": 123,
		"chat": {"id": 789, "type": "private"},
		"from": {"id": 111, "username": "testuser"},
		"text": "Hello world"
	}`

	var msg TelegramMessage
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.MessageID != 123 {
		t.Errorf("MessageID = %d, want 123", msg.MessageID)
	}
	if msg.Chat.ID != 789 {
		t.Errorf("Chat.ID = %d, want 789", msg.Chat.ID)
	}
	if msg.Chat.Type != "private" {
		t.Errorf("Chat.Type = %q, want private", msg.Chat.Type)
	}
	if msg.From.Username != "testuser" {
		t.Errorf("From.Username = %q, want testuser", msg.From.Username)
	}
	if msg.Text != "Hello world" {
		t.Errorf("Text = %q, want 'Hello world'", msg.Text)
	}
}

// TestSplitMessage tests message splitting logic
func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxLen   int
		expected int // number of parts
	}{
		{"short", "hello", 100, 1},
		{"exact", "hello", 5, 1},
		{"split needed", "hello world foo bar", 10, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := splitMessage(tt.text, tt.maxLen)
			if len(parts) != tt.expected {
				t.Errorf("splitMessage(%q, %d) returned %d parts, want %d", tt.text, tt.maxLen, len(parts), tt.expected)
			}
		})
	}
}

// TestTelegramResponseJSON tests TelegramResponse JSON parsing
func TestTelegramResponseJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantOK  bool
		wantErr string
	}{
		{
			name:   "success response",
			json:   `{"ok": true, "result": {}}`,
			wantOK: true,
		},
		{
			name:    "error response",
			json:    `{"ok": false, "description": "Bad Request"}`,
			wantOK:  false,
			wantErr: "Bad Request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp TelegramResponse
			if err := json.Unmarshal([]byte(tt.json), &resp); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if resp.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v", resp.OK, tt.wantOK)
			}
			if resp.Description != tt.wantErr {
				t.Errorf("Description = %q, want %q", resp.Description, tt.wantErr)
			}
		})
	}
}

// TestReplyToMessage tests nested message parsing
func TestReplyToMessage(t *testing.T) {
	jsonStr := `{
		"message_id": 100,
		"text": "Reply text",
		"chat": {"id": 123, "type": "private"},
		"from": {"id": 456, "username": "user"},
		"reply_to_message": {
			"message_id": 99,
			"text": "Original text",
			"chat": {"id": 123, "type": "private"},
			"from": {"id": 456, "username": "user"}
		}
	}`

	var msg TelegramMessage
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.ReplyToMessage == nil {
		t.Fatal("ReplyToMessage should not be nil")
	}
	if msg.ReplyToMessage.MessageID != 99 {
		t.Errorf("ReplyToMessage.MessageID = %d, want 99", msg.ReplyToMessage.MessageID)
	}
	if msg.ReplyToMessage.Text != "Original text" {
		t.Errorf("ReplyToMessage.Text = %q, want 'Original text'", msg.ReplyToMessage.Text)
	}
}

// Helper function
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
