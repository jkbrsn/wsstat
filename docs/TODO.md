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
- MacOS support
  - Homebrew tap
    - Initially self-maintained, e.g. new repo `github.com/jkbrsn/homebrew-wsstat` + `brew tap-new jkbrsn/wsstat` etc.
