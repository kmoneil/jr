#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["tiktoken>=0.7"]
# ///
"""Measure what each output format costs — to the model, and to the parser.

Spec §12.2 left the list default open and said to settle it by measuring a real
hundred-issue payload rather than by taste. This is the measurement. It asks
the Go test for the payloads — so what gets counted is what `jr` emits, not a
sample somebody typed — and reports two things about each.

Tokens are what the payload costs a model that reads it. Two encodings are
reported because no tokenizer is the tokenizer; cl100k_base and o200k_base
disagree about this content by well under a percent, which is the useful
finding — the ratio is a property of the framing, not of whose vocabulary is
counting it.

Parse time is what it costs the process that reads it, which for a tool built
for scripts first runs on every invocation whether or not a model is involved.
That half is a plain Go benchmark and needs no network:

    go test ./internal/resource/issue/ -run '^$' -bench ParseCost -benchmem

Run the whole report with `make cost`. It is not part of `make ci`, because
fetching a tokenizer means touching the network and no test is allowed to.
"""

import pathlib
import re
import subprocess
import sys
import tempfile

import tiktoken

# The test that builds the payloads, and the flag that makes it write them out.
PACKAGE = "./internal/resource/issue/"
TESTS = "TestFormatCostFavoursTSVForCollections|TestFormatCostIsNotTheArgumentForARecord"
BENCHMARK = "ParseCost"
BENCHTIME = "2s"

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


# `BenchmarkParseCost/xml-10  3290  727463 ns/op  48.15 MB/s  345060 B/op  9257 allocs/op`
BENCH_LINE = re.compile(
    r"^Benchmark\w+/(\w+)\S*\s+\d+\s+"
    r"([\d.]+) ns/op\s+([\d.]+) MB/s\s+(\d+) B/op\s+(\d+) allocs/op"
)


def parse_table() -> None:
    """Run the parse benchmark and print what a consumer pays to read each format."""
    root = pathlib.Path(__file__).resolve().parent.parent
    result = subprocess.run(
        ["go", "test", PACKAGE, "-run", "^$", "-bench", BENCHMARK,
         "-benchmem", f"-benchtime={BENCHTIME}"],
        cwd=root,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.exit(f"benchmark failed:\n{result.stdout}{result.stderr}")

    rows = [m.groups() for line in result.stdout.splitlines() if (m := BENCH_LINE.match(line))]
    if not rows:
        sys.exit(f"no benchmark rows parsed from:\n{result.stdout}")

    print("\nparsing issue list --limit 100   (typed decode, per call)\n")
    print(f"{'format':<6} {'time':>10} {'vs tsv':>7} {'throughput':>12} "
          f"{'allocated':>11} {'allocs':>8}")
    base = float(rows[0][1])
    for fmt, ns, mbs, bytes_op, allocs in rows:
        print(f"{fmt:<6} {f'{float(ns) / 1000:.1f}us':>10} "
              f"{f'{float(ns) / base:.1f}x':>7} {f'{float(mbs):.1f} MB/s':>12} "
              f"{f'{int(bytes_op) / 1024:.1f} KB':>11} {int(allocs):8d}")

    print(f"\nGo {BENCHMARK}, -benchtime={BENCHTIME}. The spread is far wider than the token\n"
          "spread, and on a base small enough that one HTTP round trip dwarfs all of it.\n"
          "What travels is the garbage: YAML allocates 22x the payload to read it.")


def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        into = pathlib.Path(tmp)
        dump(into)
        table(into)
    parse_table()


if __name__ == "__main__":
    main()
