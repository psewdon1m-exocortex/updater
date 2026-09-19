#!/usr/bin/env python3
"""Exercise the real demo binary through a Linux PTY; no service mutations.

Usage: python3 scripts/test-tui-pty.py /path/to/updater [--output DIRECTORY]
Only the Python standard library is required. Real Termius acceptance is separate.
"""
import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import termios
import time


class Terminal:
    def __init__(self, binary, terminal="xterm-256color", columns=80, rows=24):
        self.master, self.slave = pty.openpty()
        self.initial = termios.tcgetattr(self.slave)
        self.data = bytearray()
        self.mark = 0
        self.set_size(columns, rows)
        self.process = subprocess.Popen(
            [binary, "tui", "--demo", "--no-color"],
            stdin=self.slave, stdout=self.slave, stderr=self.slave,
            env={**os.environ, "TERM": terminal}, start_new_session=True,
        )

    def set_size(self, columns, rows):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        if hasattr(self, "process"):
            self.process.send_signal(signal.SIGWINCH)

    def read(self, seconds=0.15):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.master], [], [], max(0, deadline-time.monotonic()))
            if not ready:
                break
            try:
                chunk = os.read(self.master, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            self.data.extend(chunk)

    def send(self, value):
        self.read()
        self.mark = len(self.data)
        os.write(self.master, value)
        self.read()

    def expect(self, text, seconds=6):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            self.read()
            if text.encode() in self.data[self.mark:]:
                return
        raise AssertionError(f"Missing {text!r} in terminal output:\n{self.plain()[-5000:]}")

    def plain(self):
        text = self.data.decode("utf-8", errors="replace")
        return re.sub(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[=>])", "", text)

    def finish(self, signum=None):
        if signum is None:
            self.send(b"\x03")
        else:
            self.process.send_signal(signum)
        self.read(0.3)
        result = self.process.wait(timeout=5)
        assert result == 0, f"Console exited with {result}"
        assert termios.tcgetattr(self.slave) == self.initial, "Terminal settings were not restored"
        assert b"\x1b[?1049l" in self.data, "Alternate screen was not released"
        os.close(self.master)
        os.close(self.slave)

    def abort(self):
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait()
        for descriptor in (self.master, self.slave):
            try:
                os.close(descriptor)
            except OSError:
                pass


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    checks = []
    terminal = Terminal(binary)
    try:
        terminal.expect("SERVICE APPLICATIONS")
        terminal.expect("CONNECTED")
        terminal.send(b"\x1b[B\r")
        terminal.expect("Running version: 0.1.8")
        terminal.send(b"\x1b[B\r")
        terminal.expect("SELECT SERVICE")
        terminal.send(b"\r")
        terminal.expect("Available: 0.1.9")
        terminal.send(b"\r")
        terminal.expect("CONFIRM / Neptune")
        terminal.send(b"\r")  # Default Cancel must return without installation.
        terminal.expect("Running version: 0.1.8")
        checks.append("arrow navigation, release check, default cancel")

        terminal.send(b"\x1b[B\r")
        terminal.expect("SELECT SERVICE")
        terminal.send(b"\r")
        terminal.expect("Available: 0.1.9")
        terminal.send(b"\r")
        terminal.expect("CONFIRM / Neptune")
        terminal.send(b"\x1b[B\r")
        terminal.expect("State: COMPLETED")
        terminal.expect("DEMO: simulated completion")
        checks.append("explicit confirmation and operation receipt")

        terminal.set_size(40, 16)
        terminal.read(0.5)
        terminal.send(b"\x1b")
        terminal.expect("Running version: 0.1.9")
        terminal.send(b"\x1b")
        terminal.expect("SERVICE APPLICATIONS")
        terminal.send(b"\x1b[B\r")  # Gryphon, initially absent.
        terminal.expect("Gryphon")
        terminal.send(b"\x1b[B" * 4 + b"\r")
        terminal.expect("SELECT SERVICE")
        terminal.send(b"\r")
        terminal.expect("Bot alias")
        terminal.send(b"test-bot\r")
        secret = b"123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"
        terminal.send(b"\x1b[200~" + secret + b"\x1b[201~")
        terminal.set_size(32, 12)
        terminal.read(0.4)
        assert secret not in terminal.data, "Pasted credential echoed to terminal"
        terminal.send(b"\r\r")
        terminal.expect("CONFIRM / Gryphon")
        terminal.send(b"\x1b")
        terminal.expect("Refresh status")
        terminal.set_size(100, 32)
        terminal.read(0.3)
        terminal.finish()
        checks.append("80x24, 40x16, 32x12, 100x32 resize, masked paste, cancellation")
        checks.append("Ctrl+C restores termios and alternate screen")
    except BaseException:
        terminal.abort()
        if args.output:
            args.output.mkdir(parents=True, exist_ok=True)
            (args.output / "pty-failure.txt").write_text(terminal.plain(), encoding="utf-8")
        raise

    legacy = Terminal(binary, terminal="vt100", columns=40, rows=16)
    try:
        legacy.expect("SERVICE APPLICATIONS")
        legacy.send(b"\x1bOB\r")  # Application-cursor mode arrow.
        legacy.expect("Running version: 0.1.8")
        legacy.finish(signal.SIGTERM)
        checks.append("vt100 application-cursor arrows and SIGTERM restoration")
    except BaseException:
        legacy.abort()
        raise

    plain = subprocess.run([binary, "tui", "--demo"], input=b"", capture_output=True, timeout=5)
    assert plain.returncode != 0 and b"interactive terminal" in plain.stderr
    assert b"\x1b[?1049h" not in plain.stdout
    checks.append("non-PTY invocation fails without entering raw mode")
    result = {"result": "PASS", "checks": checks, "termius": "NOT RUN: real client acceptance is separate"}
    if args.output:
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / "pty-report.json").write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8")
        (args.output / "pty-transcript.txt").write_text(terminal.plain(), encoding="utf-8")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
