## Documentation rules

- **Keep the README Docker section current.** Any time a new environment variable, CLI flag, or feature is added, update the Quick start, Configuration table, and Compose example in README.md as part of the same PR.

## Git rules

- **Never run `git push` unless the user explicitly says to push.** This applies even during ship workflows — stop before the push step and wait for confirmation.
- **Never commit `.claude/` directory files** as part of feature work. Any `.claude/` changes belong on a dedicated branch.

## gstack

Use the `/browse` skill from gstack for all web browsing. Never use `mcp__claude-in-chrome__*` tools.

If gstack skills aren't working, run `cd .claude/skills/gstack && ./setup` to build the binary and register skills.

Available gstack skills:
/office-hours, /plan-ceo-review, /plan-eng-review, /plan-design-review, /design-consultation, /design-shotgun, /review, /ship, /land-and-deploy, /canary, /benchmark, /browse, /connect-chrome, /qa, /qa-only, /design-review, /setup-browser-cookies, /setup-deploy, /retro, /investigate, /document-release, /codex, /cso, /autoplan, /careful, /freeze, /guard, /unfreeze, /gstack-upgrade
