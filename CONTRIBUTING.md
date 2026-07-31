# Contributing to xbot

Thanks for your interest in contributing to xbot! This guide will help you get
started.

## Quick start

```bash
# Clone and build
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build          # build xbot (server + runner)
make ci             # full CI checks (lint + build + test + web)

# Install pre-commit hooks
cp scripts/pre-commit .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
```

Requirements: **Go 1.26+**, **Node.js 22+** (for Web UI).

## Before you code

1. Read `AGENTS.md` at the project root — it contains architecture notes,
   conventions, and critical gotchas.
2. Check existing [issues](https://github.com/ai-pivot/xbot/issues) and
   [PRs](https://github.com/ai-pivot/xbot/pulls) to avoid duplicate work.
3. Open an issue to discuss major changes before implementing.

## Code conventions

- **Go:** follow `gofmt` + `golangci-lint` (run `make lint`)
- **Tests:** add tests for new functionality (`make test`)
- **Error handling:** wrap errors with context (`fmt.Errorf("...: %w", err)`)
- **Logging:** use `log/slog` (structured logging)
- **Naming:** follow Go conventions (exported = PascalCase, unexported = camelCase)

## Commit messages

Use conventional commits:

```
feat: add new channel adapter
fix: resolve session switching race condition
docs: update configuration reference
refactor: decompose cliModel struct
```

## Pull requests

1. Create a feature branch from `master`
2. Write clear, tested code
3. Run `make ci` — all checks must pass
4. Update documentation if behavior changes
5. Submit a PR with a description of what changed and why

## Documentation

The docs site is bilingual (English + Chinese):

```bash
cd docs-site
hugo server -D    # local dev server
```

- English content: `content/en/`
- Chinese content: `content/zh-cn/`
- Both use the same menu structure in `hugo.toml`

## Adding features

### New tool

Create a file in `tools/` implementing the `Tool` interface. See the
[Development guide](https://ai-pivot.github.io/xbot/development/) for details.

### New channel

Create a package under `channel/`. See the Development guide.

### New skill

Create a `SKILL.md` in `~/.xbot/skills/` or `.xbot/skills/`.

## Questions?

- Open an [issue](https://github.com/ai-pivot/xbot/issues)
- Read the [documentation](https://ai-pivot.github.io/xbot/)
- Check `AGENTS.md` for architecture details
