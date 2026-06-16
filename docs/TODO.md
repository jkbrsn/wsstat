# todo

## before v3 release

- Revisit the close-handshake timeout bound. coder's `Conn.Close()` blocks up to a hard 5s
  (`waitCloseHandshake`) waiting for the peer's Close echo. Write-only peers (e.g. event-pushing
  subscription servers) never echo, so teardown/Ctrl-C stalls ~5s and slows the subscription
  tests. Decide whether to bound the graceful close to ~1s with a `CloseNow()` fallback
  (matching gorilla's old behavior) before shipping v3.

## upcoming minor

- Option to flatten structured response data, e.g. JSON, to a single line per message
- Option to log metadata when messages are received

## further ahead

- Homebrew tap
  - Initially self-maintained, e.g. new repo `github.com/jkbrsn/homebrew-wsstat` + `brew tap-new jkbrsn/wsstat` etc.
- Support setting a custom close error/close code
