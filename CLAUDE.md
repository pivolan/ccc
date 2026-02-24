# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CCC (Claude Code Companion) is a Go application that bridges Claude Code with Telegram, enabling remote session control. It runs self-hosted, letting users start/manage Claude Code sessions from Telegram, get real-time notifications, send files, voice messages, and run shell commands remotely.

**Module:** `github.com/kidandcat/ccc` | **Go:** 1.24.0 | **No external dependencies** (stdlib only)

## Build & Test Commands

```bash
make build          # Build binary (+ codesign on macOS)
make install        # Build + install to ~/bin/ccc
make clean          # Remove build artifacts
go test ./...       # Run all tests
go test -run TestExtractLastTurn  # Run a single test
```

## Architecture

All source files are in the root package `main`. There are no sub-packages.

### Core Files

- **main.go** - Entry point, CLI command routing, all type definitions (Config, SessionInfo, HookData, TelegramMessage, PermissionRequest, etc.)
- **commands.go** - Primary business logic: `listen()` Telegram polling loop, `setup()` wizard, all Telegram command handlers (/new, /continue, /delete, /c, /stats, /auth), callback query handling for permission buttons, one-shot Claude queries in private chat
- **session.go** - Session lifecycle: `startSession()` creates/attaches tmux sessions with optional Telegram topics, `startDetached()` for headless sessions
- **tmux.go** - tmux integration: path initialization (finds tmux/ccc/claude binaries), `createTmuxSession()`, `runClaudeRaw()` with OAuth token, `sendToTmux()` with adaptive delay, `waitForClaude()` prompt polling
- **hooks.go** - Claude Code hook handlers: `handlePermissionHook()` (9min timeout, IPC via `/tmp/ccc-permissions/` .req/.resp files), `handleQuestionHook()` (inline keyboard buttons), `handleNotificationHook()`, `handleStopHook()` + `extractLastTurn()` (JSONL transcript parser with streaming dedup)
- **telegram.go** - Telegram Bot API wrapper: sendMessage (4000 char split), editMessage, sendFile, downloadTelegramFile, createForumTopic/deleteForumTopic, `redactTokenError()`
- **config.go** - Config load/save from `~/.ccc.json` (permissions 0600), legacy format migration, path resolution (absolute, `~/`, projects_dir-relative)
- **relay.go** - Streaming relay for files >= 50MB: token-based registration, one-time download links with 10min expiry, integrates with external relay server (default: ccc-relay.fly.dev)
- **service.go** - System service installation: launchd plist (macOS) or systemd unit (Linux)

### Key Data Flows

1. **Telegram -> Claude**: `listen()` polls updates -> matches message topic to session -> `sendToTmux()` sends text to tmux session -> Claude processes
2. **Claude -> Telegram**: Claude Code Stop hook triggers `ccc hook-stop` -> reads JSONL transcript -> `extractLastTurn()` parses/deduplicates -> sends to Telegram topic
3. **Permission flow**: PreToolUse hook -> `ccc hook-permission` creates `/tmp/ccc-permissions/{id}.req` -> returns "pending" -> `watchPermissionRequests()` sends Telegram buttons -> user clicks approve/deny -> writes `.resp` file -> hook reads response

### Conventions

- Tmux sessions are prefixed with `claude-` (e.g., `claude-myproject`)
- Dots in session names are converted to underscores for tmux compatibility
- Messages split at 4000 chars, preferring newline boundaries
- Callback data format: `"session:questionIndex:totalQuestions:optionIndex"`
- Session matching in hooks uses exact path, path prefix, or suffix matching
- `sendToTmux()` uses adaptive delay: 50ms base + 0.5ms per character
