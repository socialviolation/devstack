# 04 — A herdr pane shows the stack it controls

## Goal

In the herdr sidebar, each agent row shows the stack its pane controls,
whether that stack is up, and its URL.

## What herdr gives us (herdr 0.9.1, checked 2026-11-26)

- A process in a pane sees `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`,
  `HERDR_BIN_PATH`.
- `devstack serve --transport=stdio` runs as a child of `claude` in the
  pane, so it inherits these. (Seen in `herdr pane process-info`.)
- `herdr pane report-metadata <pane> --source <id> --token k=v` sets pane
  tokens. Max 16 keys per report, 32 per pane. `--ttl-ms` max 24h.
  Tokens are lost when the herdr server restarts.
- `[ui.sidebar.agents].rows` in `~/.config/herdr/config.toml` renders
  tokens as `$k`, with colour rules.
- `herdr pane list` returns every pane with its id and cwd.
- A plugin can declare `[[startup]]` (one-shot after server start) and
  `[[actions]]` (bindable commands).
- `~/.config/herdr/claude-context-token.py` (a Claude Code hook) already
  pushes `$ctx` this way. It owns the pane label, so devstack must not use the label.
- The devstack plugin exists: `herdr/herdr-plugin.toml` (a `[[build]]`
  check, panel panes, actions; no startup or events).

## Design: devstack pushes, the plugin repairs

### Pane binding

- The record gets `Pane string` (a herdr pane id).
- These actions set `Pane` to `$HERDR_PANE_ID`, if set: `stack create`,
  `stack up`, `stack resync`, `stack adopt`. Last caller wins.
- Calls over HTTP (factory agents) carry no pane, and set nothing.
- One pane can bind more than one stack.

### Publish

`devstack herdr publish` sets tokens on every bound pane:

| Token | Value |
|---|---|
| `stack` | stack name; `name +1` if the pane binds two |
| `stack_state` | `up`, `stopped`, `stopped-auto` |
| `stack_url` | the tailnet URL of the first member that has one |
| `stack_stop` | `14:20` — the stop deadline, only while up |

- Source id: `devstack`. It never touches other sources' tokens.
- A pane that `herdr pane list` no longer shows loses its binding.
- If herdr is absent (no binary, no socket), publish does nothing and
  exits 0.

### Triggers

- Every state change calls publish for the panes it touches: create, up,
  down, rm, sweep, resync, adopt. Best effort, never fails the command.
- The plugin's `[[startup]]` runs `devstack herdr publish`, so tokens come
  back after a herdr restart.

### Plugin additions (`herdr/herdr-plugin.toml`)

```toml
[[startup]]
command = ["devstack", "herdr", "publish"]

[[actions]]
id = "open-pane-stack"
title = "devstack: open this pane's stack"
contexts = ["pane"]
command = ["devstack", "herdr", "open"]
```

`devstack herdr open` reads the pane from `HERDR_PLUGIN_CONTEXT_JSON` and
opens `stack_url` in the browser.

### Owner config (documented, devstack never edits it)

```toml
[ui.sidebar.agents]
rows = ["$ctx", "$stack $stack_state", "$stack_url"]
# colour rules: stack_state equals "up" → green; "stopped-auto" → dim
```

## Not verified

- Whether `herdr` finds its socket from the systemd sweep timer, which runs
  outside any pane. If not, publish must read `~/.config/herdr/herdr.sock`.
- The `pane` context name for `[[actions]]`. The docs show `workspace` and
  `global`.
- Whether herdr reuses pane ids after a pane closes. If it does, a stale
  binding could show on a new pane until the next publish.

## URL rule

- `stack_url` is the tailnet HTTPS URL that `urls` reports for the stack.
  It shows even when the service does not answer. It is a sign that the
  stack is up, not a health check.
- A stack with no tailnet URL (backend only) shows no `stack_url`.
