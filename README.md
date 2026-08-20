# aiusage

A small terminal and native macOS dashboard for remaining AI quotas.

## Platforms

| Platform | CLI | Live collectors | Native app |
| --- | --- | --- | --- |
| macOS | Yes | Claude, Codex, Kimi, Z.AI | arm64, macOS 13+ |
| Linux/Unix | Yes | Claude, Codex, Kimi, Z.AI | No |
| Windows | Yes | No; cache/status-line use only | No |

Go 1.25 or newer is required. The module prefers Go 1.26.6 through its toolchain directive; Go can select and download that toolchain automatically.

## CLI

The easiest install is:

```sh
go install github.com/plopezlpz/aiusage@latest
```

Prebuilt CLI archives for Linux, macOS, and Windows are available from [GitHub Releases](https://github.com/plopezlpz/aiusage/releases); extract one and move `aiusage` (`aiusage.exe` on Windows) to a directory on `PATH`. Or build and run the labeled fixture demo:

```sh
go build -o aiusage .
./aiusage --demo
```

**Before running the live dashboard:** normal `aiusage` runs automatically refresh providers. On Unix, Claude collection sends Claude Code's raw OAuth bearer token to Anthropic's undocumented/private `https://api.anthropic.com/api/oauth/usage` endpoint. Codex and Kimi collection launch experimental local provider services. Z.AI collection exports the configured `zai-coding-cn` API key from Pi in memory and sends it to Zhipu's undocumented quota endpoint. These interfaces can change or stop working without notice.

Run the live dashboard:

```sh
./aiusage
```

## Providers

- **Claude:** In Claude Code, run `/statusline` and ask it to use `aiusage ingest-claude-code`. This documented bridge reads JSON from stdin and updates the cache. Live OAuth collection is Unix-only; macOS reads the login keychain service `Claude Code-credentials`, then `${CLAUDE_CONFIG_DIR:-~/.claude}/.credentials.json`, while other Unix systems use that private credentials file.
- **OpenAI Codex (experimental):** install Codex, run `codex login`, then use the dashboard or `aiusage collect-codex`. Unix only; it starts the Codex read-only app server and uses its account/rate-limit RPCs.
- **Kimi Code (experimental):** install and sign in to Kimi Code, then use the dashboard or `aiusage collect-kimi`. Unix only; it starts Kimi's loopback web service and reads its private `server.token` for the local request.
- **Z.AI Coding Plan China (experimental):** install Pi, run `/login zai-coding-cn`, then use the dashboard or `aiusage collect-zai`. Unix only; it asks Pi for the configured API key through `pi auth print-api-key --provider zai-coding-cn` and queries `https://open.bigmodel.cn/api/monitor/usage/quota/limit`. The key remains in memory and is not stored by aiusage.

One-shot Claude collection is `aiusage collect-claude`. The legacy `aiusage --claude-oauth` form still opens the normal dashboard.

## macOS menu-bar app

Download the app zip from [GitHub Releases](https://github.com/plopezlpz/aiusage/releases), unzip it, and move `AiUsage.app` to Applications. Release apps are ad-hoc signed, not notarized, so the first launch may require Control-click/right-click **Open**. Notarization will require Apple Developer credentials later.

To build from source, use an Apple Silicon Mac, Swift 6, and Xcode 16 or newer. The app is arm64-only, supports macOS 13+, bundles its private Go helper, and does not enable App Sandbox.

```sh
./scripts/macos.sh build
open dist/AiUsage.app
```

Install a source build into `~/Applications` with `./scripts/macos.sh install`. The installer does not add the terminal command to `PATH`; use `go install .` separately when wanted. Finder does not inherit your shell `PATH`, so the app also checks Homebrew, `~/.local/bin`, Kimi's install directory, and common Node-manager paths; link unusual CLI installs into `~/.local/bin`. Quit a running AI Usage app before replacing it.

Left-click the menu-bar icon to open the compact panel. Use Refresh or Command-R to force collection, click a quota for details, press Escape or click outside to hide, and use the visible Quit button, Command-Q, or the right-click Quit menu to exit.

## Cache and privacy

Provider caches live in the platform user-cache directory (on macOS, `~/Library/Caches/aiusage`). They contain quota values, timestamps, provider metadata, plan/tier details when returned, and sanitized failures. OAuth tokens, local-server tokens, and exported Z.AI API keys are not cached.

On Unix, the cache directory is mode `0700` and files are `0600`. Windows uses the cache/profile ACLs. Failed refreshes preserve the last successful values and mark them stale; values also become stale after 15 minutes or a passed reset. Remove the cache directory manually to clear retained data.

## Commands and controls

- `aiusage` — live terminal dashboard
- `aiusage --demo` — fixture dashboard without collectors
- `aiusage ingest-claude-code` — Claude Code status-line bridge
- `aiusage collect-claude|collect-codex|collect-kimi|collect-zai` — one-shot collection
- `aiusage dashboard-json` — cached version 1 JSON only
- `aiusage dashboard-json --refresh=auto|--refresh=force` — due or forced refresh, then JSON
- Terminal: `↑`/`↓` or `j`/`k`; `enter`/`→`/`l` details; `esc`/`←`/`h` back; `r` refresh; `q` or `ctrl+c` quit

The dashboards reload caches every minute, check providers every five minutes, and keep the required refresh five seconds after a displayed reset.

## Development

```sh
for i in 1 2 3; do go test ./... || exit; done
go test -race ./...
go vet ./...
gofmt -d *.go
go mod tidy -diff
GOOS=linux GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
swift test --package-path macos
shellcheck scripts/macos.sh
./scripts/macos.sh build
codesign --verify --deep --strict dist/AiUsage.app
```

Tests and builds do not require live provider collection.

## License

[MIT](LICENSE)
