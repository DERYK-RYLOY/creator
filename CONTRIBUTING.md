# Contributing to Creator

Thanks for wanting to make the terminal agent that never floods its context.

## Quick start

```sh
git clone https://github.com/DERYK-RYLOY/creator && cd creator
go build ./...
go test ./... -count=1
go vet ./...
gofmt -l .
```

Requires Go 1.27+. No CGO, no system dependencies.

## The one rule that shapes the codebase

**Output is pulled on demand, never pushed.** Tool output is stored as an
artifact and returned to the model as a small digest with a handle; detail is
pulled with `out()`. Anything that would push large raw output into the model's
context is a bug, even if it is convenient.

## Before you open a PR

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` all pass.
- `gofmt -l .` prints nothing new (tip: `gofmt -w .`).
- New features come with tests. The TUI has table tests and a full synthetic
  smoke test (`internal/tui/lens_test.go`) — extend them.
- Keep diffs tight. Unrelated changes belong in their own PR.

## What makes a good PR

- fixes with a failing test first, when feasible
- behavior changes with docs updated in the same PR (README or docs/)
- anything touching the policy engine, shell tools or credential handling
  deserves extra care and a clear explanation of what was verified

## Project layout

See `## Layout` in the README. `internal/tui` is mission control (model,
view, theme, palette, lenses, search). `internal/tools` is the toolset. Read
`docs/TODO.md` for how the task board works, and the repo root `install.sh` /
`install.ps1` for how releases reach users.

## Asking questions

Open a discussion or a small issue — "is this a bug?" is a fine question.
