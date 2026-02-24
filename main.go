package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

<<<<<<< HEAD
const version = "1.6.2"
=======
const version = "2.0.0"
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)

// Config stores bot configuration (in-memory only, no persistence)
type Config struct {
<<<<<<< HEAD
	BotToken         string                  `json:"bot_token"`
	ChatID           int64                   `json:"chat_id"`                     // Private chat for simple commands
	GroupID          int64                   `json:"group_id,omitempty"`          // Group with topics for sessions
	Sessions         map[string]*SessionInfo `json:"sessions,omitempty"`          // session name -> session info
	ProjectsDir      string                  `json:"projects_dir,omitempty"`      // Base directory for new projects (default: ~)
	TranscriptionLang string                  `json:"transcription_lang,omitempty"` // Language code for whisper (e.g. "es", "en")
	RelayURL         string                  `json:"relay_url,omitempty"`         // Relay server URL for large file transfers
	Away             bool                    `json:"away"`
	OAuthToken       string                  `json:"oauth_token,omitempty"`
	OTPSecret        string                  `json:"otp_secret,omitempty"`        // TOTP secret for safe mode
=======
	BotToken        string
	ChatID          int64
	SkipPermissions bool
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
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

<<<<<<< HEAD
// TopicResult represents the result of creating a forum topic
type TopicResult struct {
	MessageThreadID int64  `json:"message_thread_id"`
	Name            string `json:"name"`
}

// HookData represents data received from Claude hook
type HookData struct {
	Cwd              string          `json:"cwd"`
	TranscriptPath   string          `json:"transcript_path"`
	SessionID        string          `json:"session_id"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	Prompt           string          `json:"prompt"`            // For UserPromptSubmit hook
	Message          string          `json:"message"`           // For Notification hook
	Title            string          `json:"title"`             // For Notification hook
	NotificationType string          `json:"notification_type"` // For Notification hook
	StopHookActive   bool            `json:"stop_hook_active"`  // For Stop hook
	ToolInputRaw     json.RawMessage `json:"tool_input"`        // Raw tool input JSON
	ToolInput        HookToolInput   `json:"-"`                 // Parsed from ToolInputRaw
}

// HookToolInput holds parsed tool input for known tool types
type HookToolInput struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
	Command     string `json:"command,omitempty"`     // For Bash
	Description string `json:"description,omitempty"` // For Bash
	FilePath    string `json:"file_path,omitempty"`   // For Read/Write/Edit
}

// parseHookData unmarshals raw JSON and populates ToolInput
func parseHookData(data []byte) (HookData, error) {
	var hd HookData
	if err := json.Unmarshal(data, &hd); err != nil {
		return hd, err
	}
	if len(hd.ToolInputRaw) > 0 {
		json.Unmarshal(hd.ToolInputRaw, &hd.ToolInput)
	}
	return hd, nil
}

// InlineKeyboardButton represents a Telegram inline keyboard button
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

=======
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
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

<<<<<<< HEAD
	case "config":
		config, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(os.Args) < 3 {
			// Show current config
			fmt.Printf("projects_dir: %s\n", getProjectsDir(config))
			if config.OAuthToken != "" {
				fmt.Println("oauth_token: configured")
			} else {
				fmt.Println("oauth_token: not set")
			}
			if config.TranscriptionLang != "" {
				fmt.Printf("transcription_lang: %s\n", config.TranscriptionLang)
			} else {
				fmt.Println("transcription_lang: not set (auto-detect)")
			}
			if isOTPEnabled(config) {
				fmt.Println("otp: enabled")
			} else {
				fmt.Println("otp: disabled (enable with: ccc setup <bot_token>)")
			}
			fmt.Println("\nUsage: ccc config <key> <value>")
			fmt.Println("  ccc config projects-dir ~/Projects")
			fmt.Println("  ccc config oauth-token <token>")
			fmt.Println("  ccc config transcription-lang es")
			os.Exit(0)
		}
		key := os.Args[2]
		if len(os.Args) < 4 {
			// Show specific key
			switch key {
			case "projects-dir":
				fmt.Println(getProjectsDir(config))
			case "oauth-token":
				if config.OAuthToken != "" {
					fmt.Println("configured")
				} else {
					fmt.Println("not set")
				}
			case "bot-token":
				if config.BotToken != "" {
					fmt.Println("configured")
				} else {
					fmt.Println("not set")
				}
			case "transcription-lang":
				if config.TranscriptionLang != "" {
					fmt.Println(config.TranscriptionLang)
				} else {
					fmt.Println("not set (auto-detect)")
				}
			case "otp":
				if isOTPEnabled(config) {
					fmt.Println("enabled")
				} else {
					fmt.Println("disabled")
				}
			default:
				fmt.Fprintf(os.Stderr, "Unknown config key: %s\n", key)
				os.Exit(1)
			}
			os.Exit(0)
		}
		value := os.Args[3]
		switch key {
		case "projects-dir":
			config.ProjectsDir = value
			if err := saveConfig(config); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("✅ projects_dir set to: %s\n", getProjectsDir(config))
		case "oauth-token":
			config.OAuthToken = value
			if err := saveConfig(config); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ OAuth token saved")
		case "bot-token":
			config.BotToken = value
			if err := saveConfig(config); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ Bot token saved")
		case "transcription-lang":
			config.TranscriptionLang = value
			if err := saveConfig(config); err != nil {
				fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("✅ Transcription language set to: %s\n", value)
		case "otp":
			fmt.Fprintf(os.Stderr, "Permission mode can only be changed via: ccc setup <bot_token>\n")
			os.Exit(1)
		default:
			fmt.Fprintf(os.Stderr, "Unknown config key: %s\n", key)
			os.Exit(1)
		}

	case "setgroup":
		config, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := setGroup(config); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "listen":
		if err := listen(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-permission":
		if err := handlePermissionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-question":
		// Legacy: redirect to permission hook
		if err := handlePermissionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-stop":
		if err := handleStopHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-notification":
		if err := handleNotificationHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "install":
		if err := installHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := installSkill(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := installService(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "uninstall":
		if err := uninstallHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not uninstall hooks: %v\n", err)
		}
		uninstallSkill()
		fmt.Println("✅ CCC uninstalled")

	case "send":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: ccc send <file>\n")
			os.Exit(1)
		}
		if err := handleSendFile(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "start":
		// start <name> <work-dir> <prompt>
		// Creates a Telegram topic, tmux session with Claude, and sends the prompt (detached)
		if len(os.Args) < 5 {
			fmt.Fprintf(os.Stderr, "Usage: ccc start <session-name> <work-dir> <prompt>\n")
			os.Exit(1)
		}
		if err := startDetached(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "relay":
		port := "8080"
		if len(os.Args) >= 3 {
			port = os.Args[2]
		}
		runRelayServer(port)

	default:
		if err := send(strings.Join(os.Args[1:], " ")); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
=======
	config := &Config{BotToken: token, ChatID: chatID, SkipPermissions: skipPerms}
	if err := run(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
>>>>>>> bf46e03 (refactor: simplify architecture, remove external dependencies)
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
