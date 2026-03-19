package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const version = "2.0.0"

// Config stores bot configuration (in-memory only, no persistence)
type Config struct {
	BotToken        string
	ChatID          int64
	SkipPermissions bool
	GroupMode       bool
	GroupID         int64 // used in hook subprocesses
	TopicID         int   // used in hook subprocesses
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
	Text              string            `json:"text"`
	MessageThreadID   int               `json:"message_thread_id,omitempty"`
	IsTopicMessage    bool              `json:"is_topic_message,omitempty"`
	ForumTopicCreated *struct{}         `json:"forum_topic_created,omitempty"`
	ReplyToMessage    *TelegramMessage  `json:"reply_to_message,omitempty"`
	Voice             *TelegramVoice    `json:"voice,omitempty"`
	Photo             []TelegramPhoto   `json:"photo,omitempty"`
	Document          *TelegramDocument `json:"document,omitempty"`
	Caption           string            `json:"caption,omitempty"`
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
		case "hook-pretooluse":
			handlePreToolUseHook()
			return
		case "hook-posttooluse":
			handlePostToolUseHook()
			return
		case "tg-send":
			handleTgSend()
			return
		}
	}

	// Parse remaining args: token and optional flags
	var token string
	var chatIDFlag string
	skipPerms := false
	groupMode := false
	for _, arg := range os.Args[1:] {
		if arg == "--yolo" {
			skipPerms = true
		} else if arg == "--group" {
			groupMode = true
		} else if strings.HasPrefix(arg, "--chat-id=") {
			chatIDFlag = strings.TrimPrefix(arg, "--chat-id=")
		} else if !strings.HasPrefix(arg, "-") {
			token = arg
		}
	}

	if token == "" {
		printHelp()
		os.Exit(1)
	}

	config := &Config{BotToken: token, SkipPermissions: skipPerms, GroupMode: groupMode}

	if chatIDFlag != "" {
		chatID, err := strconv.ParseInt(chatIDFlag, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid --chat-id: %s\n", chatIDFlag)
			os.Exit(1)
		}
		config.ChatID = chatID
		fmt.Printf("Using preset Chat ID: %d\n", chatID)
	} else if groupMode {
		fmt.Println("Starting in group mode...")
		config.ChatID = 0
	} else {
		fmt.Println("Send any message to your bot in Telegram...")
		chatID, err := waitForFirstMessage(token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Connected! Chat ID: %d\n", chatID)
		config.ChatID = chatID
	}

	if err := run(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var photoExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

var videoExtensions = map[string]bool{
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
}

func handleTgSend() {
	var token, chatStr, caption, target string
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--token=") {
			token = strings.TrimPrefix(arg, "--token=")
		} else if strings.HasPrefix(arg, "--chat-id=") {
			chatStr = strings.TrimPrefix(arg, "--chat-id=")
		} else if strings.HasPrefix(arg, "--caption=") {
			caption = strings.TrimPrefix(arg, "--caption=")
		} else if !strings.HasPrefix(arg, "-") {
			target = arg
		}
	}

	if token == "" || chatStr == "" || target == "" {
		fmt.Fprintln(os.Stderr, "Usage: ccc tg-send --token=TOKEN --chat-id=CHATID [--caption=TEXT] <file_or_message>")
		os.Exit(1)
	}

	chatID, err := strconv.ParseInt(chatStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid chat-id: %s\n", chatStr)
		os.Exit(1)
	}

	config := &Config{BotToken: token, ChatID: chatID}

	// Check if target is a file
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		ext := strings.ToLower(filepath.Ext(target))
		if photoExtensions[ext] {
			err = sendPhoto(config, chatID, target, caption)
		} else if videoExtensions[ext] {
			err = sendVideo(config, chatID, target, caption)
		} else {
			err = sendFile(config, chatID, target, caption)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending file: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("File sent successfully")
		return
	}

	// Otherwise treat target as text message
	text := target
	if caption != "" {
		text = caption + "\n\n" + target
	}
	if err := sendMessage(config, chatID, text); err != nil {
		fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Message sent successfully")
}

func printHelp() {
	fmt.Printf(`ccc - Claude Code Companion v%s

USAGE:
    ccc <bot_token>          Start bot (creates tmux + Claude, polls Telegram)
    ccc <bot_token> --yolo   Start with auto-accept all permissions
    ccc <bot_token> --group  Start in group mode (topics = sessions)
    ccc tg-send              Send file/message to Telegram (used by Claude)

FLAGS:
    -h, --help               Show this help
    -v, --version            Show version
    --yolo                   Skip all Claude permission prompts
    --group                  Enable group mode with topic-based sessions
    --chat-id=ID             Pre-set authorized chat ID (skip first-message wait)

TELEGRAM COMMANDS:
    /c <cmd>                Execute shell command
    /restart                Restart Claude session (current topic or main)
    /topic <name> <path>    Create a new topic with its own Claude session
    /stats                  Show system stats
    /version                Show version

TG-SEND:
    ccc tg-send --token=TOKEN --chat-id=CHATID [--caption=TEXT] <file_or_message>

For more info: https://github.com/kidandcat/ccc
`, version)
}
