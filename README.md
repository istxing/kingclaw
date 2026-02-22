<div align="center">

  <h1>KingClaw</h1>
  <p><strong>Lightweight Personal AI Assistant in Go</strong></p>

  <p>
    <img src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go&logoColor=white" alt="Go">
    <img src="https://img.shields.io/badge/License-MIT-green" alt="License">
    <img src="https://img.shields.io/badge/Platforms-Linux%20x86__64%20%7C%20ARM64%20%7C%20RISC--V-blue" alt="Platforms">
  </p>

  <p>
    <a href="https://github.com/istxing/kingclaw">GitHub</a> ·
    <a href="README.zh.md">中文文档</a>
  </p>
</div>

---

> [!IMPORTANT]
> KingClaw is independently maintained at `https://github.com/istxing/kingclaw`.
> There is currently no official project domain. Use this repository as the only official source for releases, issues, and documentation.

## What Is KingClaw

KingClaw is a local-first, lightweight AI assistant built in Go.
It targets constrained devices and practical automation workloads with:

- fast startup
- low runtime overhead
- single-binary deployment
- CLI and gateway operation modes

## Core Capabilities

- `agent`: direct conversation with tools
- `gateway`: long-running chat bot gateway (Telegram/Discord/Slack/LINE/WeCom/...) 
- `skills`: install, list, search, remove skill packs
- `cron`: scheduled tasks and reminders
- `auth`: provider authentication (including OAuth flows)
- `onboard`: initialize config and workspace

## Quick Start

### 1. Clone

```bash
git clone https://github.com/istxing/kingclaw.git
cd kingclaw
```

### 2. Install Dependencies

```bash
make deps
```

### 3. Initialize

```bash
go run ./cmd/kingclaw onboard
# or after build:
# kingclaw onboard
```

### 4. Configure API Provider

Edit:

- `~/.kingclaw/config.json`

A template exists at:

- `config/config.example.json`

### 5. Run

```bash
# one-shot
kingclaw agent -m "Hello"

# interactive
kingclaw agent
```

## Build

```bash
# build current platform
make build

# build all configured targets
make build-all
```

Default output:

- `build/kingclaw`

## Docker Compose

`docker-compose.yml` includes two profiles:

- `kingclaw-agent`: one-shot/interactive agent
- `kingclaw-gateway`: long-running gateway service

### Start Gateway

```bash
cp config/config.example.json config/config.json
# edit config/config.json

docker compose --profile gateway up -d
docker compose logs -f kingclaw-gateway
```

### Run Agent Once

```bash
docker compose run --rm kingclaw-agent -m "2+2?"
```

## Workspace Layout

Default workspace:

- `~/.kingclaw/workspace`

Typical content:

- `memory/` runtime memory files
- `skills/` installed skills
- `cron/` scheduled task data

## Common Commands

```bash
kingclaw onboard
kingclaw status
kingclaw agent
kingclaw agent -m "Summarize this file"
kingclaw gateway
kingclaw auth status
kingclaw auth login --provider openai
kingclaw cron list
kingclaw skills list
kingclaw skills search "hardware"
```

## Configuration Notes

- Environment variable prefix: `KINGCLAW_`
- Home directory override: `KINGCLAW_HOME`
- Default config path: `~/.kingclaw/config.json`

Detailed configuration docs:

- `docs/tools_configuration.md`
- `docs/wecom-app-configuration.md`
- `docs/ANTIGRAVITY_AUTH.md`
- `docs/ANTIGRAVITY_USAGE.md`

## Security Statement

- KingClaw has no official token/coin.
- Treat third-party domains/channels claiming project ownership as untrusted.
- Review tool permissions and provider credentials before production deployment.

## Contributing

1. Fork this repository
2. Create a feature branch
3. Keep patches focused and tested
4. Open a PR

## License

MIT License. See `LICENSE`.
