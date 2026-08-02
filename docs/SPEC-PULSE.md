# SPEC — Pulse

Status: shipped 2026-08-02. This file is the binding contract. Pulse is the
evidence law made visible: the rail renders observed time instead of
claiming states.

## Laws (binding)

1. **Only evidence may move.** Bells blink, sparklines flow; everything
   else on the rail is still. Motion is a claim that something happened,
   so motion without evidence is forbidden.
2. **The sparkline is observation, not estimation.** Eight 8-second
   buckets per window, each counting observed `#{window_activity}`
   advances (at most one per second, the polling resolution). No rates, no
   smoothing, no interpolation — a histogram of facts.
3. **Motion while alive, age while quiet, never both.** An agent row shows
   its sparkline when any bucket is nonzero, its quiet age (`claude 26m`)
   when all are silent. A flat pulse earns no pixels; the age states the
   same evidence in fewer.
4. **No verbs.** The waveform lets the operator read "working", "stalled",
   "burst then stopped" from data. ghostmux never prints those words —
   that is the line between this and screen-scraped status labels.
5. **The frame may not assume the viewer's terminal.** Colors resolve
   through `internal/theme`: the gruvbox identity by default,
   `GHOSTMUX_THEME=ansi` for the terminal's own ANSI-16 palette — the
   panel in the operator's rice, on every box they attach from. Env-only
   for now: the mode is read before any color initializes; a settings
   switch requires the var→lookup refactor and is deliberately deferred.

## Mechanics

- `pulseRing` per stable window ID, maintained in `observeActivity`
  alongside the acknowledgement ledger; rings die with their windows.
- Rendered on agent rows (ambient `agentCmds` detection) in the command
  slot: `claude ▂▅█▃▁ ▁▁▁`. Levels ` ▁▂▃▄▅▆▇█` map counts 0–8; zero is a
  gap because measured silence renders as silence.
- Non-agent rows keep their quiet command suffix — the fleet stays calm;
  the things you steer are the things that breathe.

## Key code

- `internal/rail/activity.go` — `pulseRing`
- `internal/rail/rows.go` — `sparkline`, `pulseGlyphs`
- `internal/rail/view.go` — the spark-or-age rule
- `internal/theme/theme.go` — the palette seam
