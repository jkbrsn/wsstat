# TODO

## Upcoming minor

- Option to log metadata when messages are received

## Further ahead

- CLI: deferred past v3.0.0 (add only when a concrete need appears, YAGNI)
  - `--clip-width N` override (ship `--clip` boolean first)
  - `-vvv` level-3 verbosity (current ladder is content-bounded; needs a custom counter `flag.Value`)
  - raw stream framing opt-in: `--delimiter` / `--print0`
  - JSON output enrichment: `--include headers,certs` / `--detail full` (keep `-o json` schema-stable)
  - shell completion (`completion` subcommand, bash/zsh/fish) — no requests yet; static per-shell
    scripts completing subcommands/flags/enum values is the likely shape when it lands
- lib: `subscription-matcher` — export frame demultiplexing so one connection can carry several
  subscriptions. `SubscriptionOptions.matcher`/`.decoder` already exist and `dispatchIncoming`
  already honors them, but nothing outside the package can set them, so every external
  subscription claims all frames and `Subscribe` returns `ErrSubscriptionConflict` for the
  second one. Exporting `Matcher` (and likely `Decoder`) makes multi-subscription connections
  reachable; public surface addition, so v4 — see `docs/plans/v4-api-plan.md`. Open questions
  for when it lands: whether unmatched frames fall through to `readChan` or are dropped, and
  how the per-subscription counters attribute a frame that several matchers claim.
- MacOS support
  - Homebrew tap
    - Initially self-maintained, e.g. new repo `github.com/jkbrsn/homebrew-wsstat` + `brew tap-new jkbrsn/wsstat` etc.
