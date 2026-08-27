#!/usr/bin/env python3
"""Drive the TUI inside a pseudo-terminal and print what the screen shows.

A full-screen terminal program can only really be checked by running it against
a terminal. This spawns the binary on a pty of a fixed size, feeds it
keystrokes, and renders the resulting screen through a terminal emulator so the
output can be read (and diffed) as plain text.
"""
import codecs
import os
import pty
import select
import shlex
import signal
import sys
import time

import pyte

# Named keys, so scripts read as intent rather than as escape codes.
KEYS = {
    "<up>": "\x1b[A", "<down>": "\x1b[B", "<right>": "\x1b[C", "<left>": "\x1b[D",
    "<enter>": "\r", "<esc>": "\x1b", "<tab>": "\t", "<btab>": "\x1b[Z",
    "<space>": " ", "<bs>": "\x7f", "<home>": "\x1b[H", "<end>": "\x1b[F",
    "<pgup>": "\x1b[5~", "<pgdn>": "\x1b[6~",
    "<c-s>": "\x13", "<c-c>": "\x03", "<c-d>": "\x04", "<c-u>": "\x15",
    "<c-r>": "\x12", "<c-w>": "\x17",
}


def expand(s):
    out, i = [], 0
    while i < len(s):
        if s[i] == "<":
            j = s.find(">", i)
            if j > 0 and s[i:j + 1].lower() in KEYS:
                out.append(KEYS[s[i:j + 1].lower()])
                i = j + 1
                continue
        out.append(s[i])
        i += 1
    return "".join(out)


def split_keys(s):
    """Split an expanded key script into individual keypresses."""
    out, i = [], 0
    while i < len(s):
        if s[i] == "<":
            j = s.find(">", i)
            if j > 0 and s[i:j + 1].lower() in KEYS:
                out.append(KEYS[s[i:j + 1].lower()])
                i = j + 1
                continue
        out.append(s[i])
        i += 1
    return out


def main():
    if len(sys.argv) < 2:
        print("usage: drive.py 'command' [WIDTHxHEIGHT] [keys...]", file=sys.stderr)
        return 2
    cmd = shlex.split(sys.argv[1])
    size = sys.argv[2] if len(sys.argv) > 2 else "120x40"
    cols, rows = (int(v) for v in size.lower().split("x"))
    steps = sys.argv[3:]

    screen = pyte.Screen(cols, rows)
    stream = pyte.Stream(screen)
    # Box-drawing characters are multi-byte, and a read can land in the middle
    # of one. An incremental decoder holds the partial sequence back until the
    # rest arrives; decoding each chunk independently corrupts the stream and
    # desynchronises the emulator.
    decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["COLORTERM"] = "truecolor"
        os.environ["LINES"] = str(rows)
        os.environ["COLUMNS"] = str(cols)
        os.execvp(cmd[0], cmd)

    import fcntl, struct, termios
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def answer_queries(text):
        """Reply to the terminal capability probes the program sends on start.

        A TUI asks the terminal for its background colour and cursor position
        and then waits for the answers. Without a real terminal behind the pty
        nothing replies, and the program hangs before drawing anything.
        """
        if "\x1b]11;?" in text or "\x1b]11;?\x07" in text:
            os.write(fd, b"\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\")
        if "\x1b]10;?" in text:
            os.write(fd, b"\x1b]10;rgb:cdcd/d6d6/f4f4\x1b\\")
        if "\x1b[6n" in text:
            os.write(fd, b"\x1b[1;1R")
        if "\x1b[>q" in text or "\x1b[>0q" in text:
            os.write(fd, b"\x1bP>|pyte\x1b\\")

    def drain(timeout=0.6):
        deadline = time.time() + timeout
        while time.time() < deadline:
            r, _, _ = select.select([fd], [], [], max(0, deadline - time.time()))
            if not r:
                continue
            try:
                data = os.read(fd, 65536)
            except OSError:
                return
            if not data:
                return
            text = decoder.decode(data)
            answer_queries(text)
            stream.feed(text)
            deadline = time.time() + 0.25

    def dump(label):
        print(f"\n===== {label} =====")
        for line in screen.display:
            print(line.rstrip())

    drain(1.5)
    dump("initial")

    for step in steps:
        if step.startswith("sleep:"):
            time.sleep(float(step.split(":", 1)[1]))
            drain(0.3)
            continue
        label, _, keys = step.partition("=")
        if not keys:
            label, keys = step, step
        # Send one key at a time. A terminal program coalesces printable bytes
        # that arrive in a single read into one paste-like event, so writing a
        # whole string at once does not exercise the same code path a person
        # typing would.
        for key in split_keys(keys):
            os.write(fd, key.encode())
            drain(0.04)
        # Let the redraw settle before reading the screen, so the dump is a
        # finished frame rather than a partially repainted one.
        drain(0.5)
        time.sleep(0.15)
        drain(0.3)
        dump(label)

    os.write(fd, b"\x03")
    time.sleep(0.2)
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    os.close(fd)
    return 0


if __name__ == "__main__":
    sys.exit(main())
