#!/usr/bin/env python3
"""Run a command under a pseudo-terminal of a fixed window size.

The soak harness uses this to exercise wsstat's TTY-only behaviors (--color auto,
--clip) deterministically: a plain pipe makes stdout a non-terminal, so those
features no-op and their effect can't be observed. `script(1)` gives a PTY but
won't let us pin its column count, and --clip reads the width via the TIOCGWINSZ
ioctl (not $COLUMNS), so we set TIOCSWINSZ on the master fd right after fork.

Usage: pty-run.py COLS ROWS -- CMD [ARG...]
Streams the child's PTY output to our stdout and exits with the child's status.
"""
import fcntl
import os
import pty
import struct
import sys
import termios


def main() -> int:
    argv = sys.argv[1:]
    if len(argv) < 4 or argv[2] != "--":
        sys.stderr.write("usage: pty-run.py COLS ROWS -- CMD [ARG...]\n")
        return 2
    cols, rows = int(argv[0]), int(argv[1])
    cmd = argv[3:]

    pid, fd = pty.fork()
    if pid == 0:  # child: stdout/stderr are the slave PTY
        os.execvp(cmd[0], cmd)
        os._exit(127)  # unreachable on success

    # Parent: pin the window size before the child renders anything.
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    out = sys.stdout.buffer
    while True:
        try:
            chunk = os.read(fd, 65536)
        except OSError:  # slave closed -> EIO on Linux
            break
        if not chunk:
            break
        try:
            out.write(chunk)
            out.flush()
        except BrokenPipeError:  # downstream (e.g. grep -q) closed early
            break

    _, status = os.waitpid(pid, 0)
    if os.WIFEXITED(status):
        return os.WEXITSTATUS(status)
    if os.WIFSIGNALED(status):
        return 128 + os.WTERMSIG(status)
    return 1


if __name__ == "__main__":
    sys.exit(main())
