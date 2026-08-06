#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["tiktoken>=0.7"]
# ///
"""Measure what each output format costs, in tokens.

Spec §12.2 left the list default open and said to settle it by measuring a real
hundred-issue payload rather than by taste. This is the measurement. It asks
the Go test for the payloads — so what gets counted is what `jr` emits, not a
sample somebody typed — and tokenizes each one.

Run it with `make cost`. It is not part of `make ci`: it fetches a tokenizer,
and nothing in the test suite is allowed to touch the network.

Two encodings are reported because no tokenizer is the tokenizer. cl100k_base
and o200k_base disagree about this content by well under a percent, which is
the useful finding — the ratio is a property of the framing, not of whose
vocabulary is counting it.
"""

import pathlib
import subprocess
import sys
import tempfile

import tiktoken

# The test that builds the payloads, and the flag that makes it write them out.
PACKAGE = "./internal/resource/issue/"
TESTS = "TestFormatCostFavoursTSVForCollections|TestFormatCostIsNotTheArgumentForARecord"

ENCODINGS = ("cl100k_base", "o200k_base")
FORMATS = ("tsv", "xml", "json", "yaml")

# Each payload, and how many rows of data it holds.
PAYLOADS = (
    ("issues-100", "issue list --limit 100", 100),
    ("issue-one", "issue get ENG-100", 1),
)


def dump(into: pathlib.Path) -> None:
    """Render every payload in every format into a directory."""
    root = pathlib.Path(__file__).resolve().parent.parent
    result = subprocess.run(
        ["go", "test", PACKAGE, "-count=1", "-run", TESTS, f"-cost-dump={into}"],
        cwd=root,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.exit(f"go test failed:\n{result.stdout}{result.stderr}")


def table(into: pathlib.Path) -> None:
    encoders = {name: tiktoken.get_encoding(name) for name in ENCODINGS}

    for stem, command, rows in PAYLOADS:
        print(f"\n{command}   ({rows} row{'s' if rows != 1 else ''})\n")
        header = f"{'format':<6} {'bytes':>7} {'tokens':>8} {'vs tsv':>7} {'per row':>8}"
        for name in ENCODINGS[1:]:
            header += f" {name:>12}"
        print(header)

        base = None
        for fmt in FORMATS:
            path = into / f"{stem}.{fmt}"
            text = path.read_text(encoding="utf-8")
            counts = {name: len(enc.encode(text)) for name, enc in encoders.items()}
            first = counts[ENCODINGS[0]]
            base = base or first
            line = (
                f"{fmt:<6} {len(text.encode()):7d} {first:8d} "
                f"{first / base:6.2f}x {first / rows:8.1f}"
            )
            for name in ENCODINGS[1:]:
                line += f" {counts[name]:12d}"
            print(line)

        print(f"\n{ENCODINGS[0]} unless a column says otherwise.")


def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        into = pathlib.Path(tmp)
        dump(into)
        table(into)


if __name__ == "__main__":
    main()
