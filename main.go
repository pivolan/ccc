package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const version = "2.0.0"

// Config stores bot configuration (in-memory only, no persistence)
type Config struct {
	BotToken        string
	ChatID          int64
	SkipPermissions bool
}

// TelegramMessage represents a Telegram message
type TelegramMessage struct {
	MessageID int   `json:"message_id"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Text           string            `json:"text"`
	ReplyToMessage *TelegramMessage  `json:"reply_to_message,omitempty"`
	Voice          *TelegramVoice    `json:"voice,omitempty"`
	Photo          []TelegramPhoto   `json:"photo,omitempty"`
	Document       *TelegramDocument `json:"document,omitempty"`
	Caption        string            `json:"caption,omitempty"`
}

type TelegramVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
}

type TelegramPhoto struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size"`
}

type TelegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int    `json:"file_size"`
}

// TelegramUpdate represents an update from Telegram
type TelegramUpdate struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      []struct {
		UpdateID      int              `json:"update_id"`
		Message       TelegramMessage  `json:"message"`
		CallbackQuery *CallbackQuery   `json:"callback_query,omitempty"`
	} `json:"result"`
}

// CallbackQuery represents a callback from inline keyboard button
type CallbackQuery struct {
	ID   string          `json:"id"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *TelegramMessage `json:"message,omitempty"`
	Data    string           `json:"data"`
}

// TelegramResponse represents a response from Telegram API
type TelegramResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func init() {
	initPaths()
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printHelp()
			return
		case "-v", "--version", "version":
			fmt.Printf("ccc version %s\n", version)
			return
		}
	}

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "hook-stop":
			handleStopHook()
			return
		case "hook-notification":
			handleNotificationHook()
			return
		case "hook-permission":
			handlePermissionHook()
			return
		}
	}

	// Parse remaining args: token and optional flags
	var token string
	skipPerms := false
	for _, arg := range os.Args[1:] {
		if arg == "--yolo" {
			skipPerms = true
		} else if !strings.HasPrefix(arg, "-") {
			token = arg
		}
	}

	if token == "" {
		printHelp()
		os.Exit(1)
	}

	fmt.Println("Send any message to your bot in Telegram...")
	chatID, err := waitForFirstMessage(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Connected! Chat ID: %d\n", chatID)

	config := &Config{BotToken: token, ChatID: chatID, SkipPermissions: skipPerms}
	if err := run(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf(`ccc - Claude Code Companion v%s

USAGE:
    ccc <bot_token>         Start bot (creates tmux + Claude, polls Telegram)
    ccc <bot_token> --yolo  Start with auto-accept all permissions

FLAGS:
    -h, --help              Show this help
    -v, --version           Show version
    --yolo                  Skip all Claude permission prompts

TELEGRAM COMMANDS:
    /c <cmd>                Execute shell command
    /restart                Restart Claude session
    /stats                  Show system stats
    /version                Show version

For more info: https://github.com/kidandcat/ccc
`, version)
}
