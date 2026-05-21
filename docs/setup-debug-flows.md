# Live TV docs index

This directory complements `../README.md`. The README covers what the plugin is, its capabilities, and how to install it. The pages below go deeper on day-2 operation.

- [Architecture](architecture.md) — internal subsystems (refresh workers, stream proxy, scheduler, snapshot), schema, and request flows.
- [Operations](operations.md) — operator runbook for sources, channel overrides, settings, and session admin.
- [Debugging](debugging.md) — symptom-driven guide for refresh failures, dead streams, EPG gaps, and player issues.
- [API reference](api-reference.md) — every public, user, stream, and admin route, plus auth requirements.
- Historical [design spec](spec/2026-05-21-livetv-plugin-design.md) and [implementation plan](plan/2026-05-21-livetv-plugin.md) (kept for context; not the source of truth on current behaviour).

If you came here from a stack trace or an admin UI link, jump to [debugging](debugging.md) — it indexes by failure symptom.
