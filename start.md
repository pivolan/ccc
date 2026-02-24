# CCC Quick Start

## Prerequisites

- Go 1.24+
- tmux
- Claude Code (`npm install -g @anthropic-ai/claude-code`)
- Telegram bot token (from [@BotFather](https://t.me/BotFather))

## Install

```bash
git clone https://github.com/kidandcat/ccc
cd ccc
make install
```

## Single Project Setup

```bash
cd ~/Projects/myproject
ccc setup <bot_token>
```

This will:
1. Ask you to send a message to your bot in Telegram — this links your chat
2. Install Claude Code hooks (Stop, Notification, PreToolUse)
3. Install a background service (launchd on macOS, systemd on Linux)
4. Save `~/Projects/myproject` as the working directory

Done. Send any message to your bot in Telegram — it will auto-start a Claude Code session in `~/Projects/myproject` and forward your text to it.

## Multiple Projects

Each project needs its own bot (create one via @BotFather) and its own config file:

```bash
cd ~/Projects/frontend
ccc --config ~/.ccc-frontend.json setup <bot_token_1>

cd ~/Projects/backend
ccc --config ~/.ccc-backend.json setup <bot_token_2>
```

Each gets its own:
- Config file (`~/.ccc-frontend.json`, `~/.ccc-backend.json`)
- Background service (`com.ccc.ccc-frontend`, `com.ccc.ccc-backend`)
- tmux session (`claude-ccc-frontend`, `claude-ccc-backend`)
- Lock file and permissions directory

## Usage

### From Telegram

Just type in the bot's private chat — your text goes straight to Claude Code.

**Commands:**
- `/c ls -la` — run a shell command
- `/stats` — system stats (CPU, RAM, disk)
- `/auth` — re-authenticate Claude OAuth
- `/restart` — restart the ccc service
- `/version` — show version

**Files:** send a photo or document — it gets saved to the project directory and forwarded to Claude.

**Permissions:** when Claude wants to run a tool (Bash, Write, Edit), you get Approve/Deny buttons in Telegram.

### From Terminal

```bash
cd ~/Projects/myproject
ccc              # start/attach tmux session with Claude
ccc -c           # continue previous conversation
ccc send file.zip  # send a file to Telegram
```

## Configuration

```bash
ccc config                           # show current config
ccc config work-dir ~/Projects/other # change project directory
ccc config oauth-token <token>       # set OAuth token
ccc doctor                           # check everything is configured
```

## Troubleshooting

```bash
ccc doctor          # check all dependencies and config
cat ~/.ccc.log      # service logs (macOS)
journalctl --user -u ccc  # service logs (Linux)
cat /tmp/ccc-hook-debug.log  # hook debug logs
```

### Common issues

**Bot not responding:** check that the service is running (`ccc doctor`). If not, start it with `ccc listen` or reload the service.

**Claude not starting in tmux:** make sure `claude` is in PATH (`which claude`). If using OAuth, set the token with `ccc config oauth-token <token>`.

**Permission buttons not appearing:** hooks may not be installed. Run `ccc install` to reinstall them.
