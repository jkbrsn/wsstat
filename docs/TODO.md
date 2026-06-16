# todo

## before v3 release

- Revisit the close-handshake timeout bound. coder's `Conn.Close()` blocks up to a hard 5s
  (`waitCloseHandshake`) waiting for the peer's Close echo. Write-only peers that do not read
  (e.g. some event-pushing servers) never echo, so teardown/Ctrl-C can stall ~5s. Decide
  whether to bound the graceful close to ~1s with a `CloseNow()` fallback (matching gorilla's
  old behavior) before shipping v3.
- Investigate whether the 5s close stall can be exercised in the `dev/` smoke stack. The
  current mock server reads, so it echoes the Close and never triggers the stall. Add a
  write-only / non-echoing endpoint to the mock so a smoke case can observe (and bound) the
  teardown latency against a peer that ignores the closing handshake. Ties into the close-bound
  decision above.

## smoke stack

- Add `wss://` coverage to the `dev/` mock server smoke stack. The mock currently serves
  `ws://` only (port 17080); `dev/smoke-test.sh` never exercises the TLS dial path
  (`DialTLSContext`, `-insecure`/`-k`, `-no-tls`, `WithTLSConfig`). Serve TLS on 17443 and add
  smoke cases so the `wss://` handshake/timing path is validated end-to-end.

## upcoming minor

- Option to flatten structured response data, e.g. JSON, to a single line per message
- Option to log metadata when messages are received

## further ahead

- Homebrew tap
  - Initially self-maintained, e.g. new repo `github.com/jkbrsn/homebrew-wsstat` + `brew tap-new jkbrsn/wsstat` etc.
- Support setting a custom close error/close code
