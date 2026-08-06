#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["anthropic>=0.69"]
# ///
"""Measure what each output format costs — to the model, and to the parser.

Spec §12.2 left the list default open and said to settle it by measuring a real
hundred-issue payload rather than by taste. This is the measurement. It asks
the Go test for the payloads — so what gets counted is what `jr` emits, not a
sample somebody typed — and reports three things about each.

Tokens are what the payload costs the model that reads it, counted with
Anthropic's own `count_tokens` endpoint. An earlier version of this script used
tiktoken; that is OpenAI's tokenizer and undercounts Claude by 15-20% on prose
and more on structured text, which is exactly the shape being measured here.
A proxy tokenizer would have put a number in the output contract that was wrong
for the model the contract exists to serve.

Latency answers the question the token count only implies: is a response
actually quicker with a cheaper format. Time-to-first-token is dominated by
prefill, which scales with input tokens, so this is where the format ratio
should show up. Thinking is disabled and effort pinned low so what is being
timed is reading the payload rather than deciding what to say about it.

Parse time is what the payload costs the process that reads it, which for a
tool built for scripts first runs on every invocation whether or not a model is
involved. That section is a plain Go benchmark and needs no network:

    go test ./internal/resource/issue/ -run '^$' -bench ParseCost -benchmem

Run the whole report with `make cost`. It is not part of `make ci`: the token
and latency sections call the Anthropic API, and no test in this repository is
allowed to touch the network. `--skip-latency` runs the free half alone —
counting tokens is not billed, generating is.
"""

import argparse
import pathlib
import re
import statistics
import subprocess
import sys
import tempfile
import time

import anthropic

# The test that builds the payloads, and the flag that makes it write them out.
PACKAGE = "./internal/resource/issue/"
TESTS = "TestFormatCostFavoursTSVForCollections|TestFormatCostIsNotTheArgumentForARecord"
BENCHMARK = "ParseCost"
BENCHTIME = "2s"

FORMATS = ("tsv", "xml", "json", "yaml")

# Each payload, and how many rows of data it holds.
PAYLOADS = (
    ("issues-100", "issue list --limit 100", 100),
    ("issue-one", "issue get ENG-100", 1),
)

# The latency task. It has to require reading every row — a question answerable
# from the first screenful would time the envelope rather than the payload —
# and its answer has to be checkable, so a format that is fast because the model
# gave up is not recorded as a win.
#
# An absent assignee is also where the formats differ most: TSV writes nothing
# between two tabs, and the others still spell the field.
TASK = (
    "The data below is the output of a Jira CLI. List the issue keys of every "
    "issue that has no assignee, separated by commas, newest key first. Output "
    "the keys and nothing else — no preamble, no explanation."
)

# The corpus assigns costAssignees[i%3] to the issue at index i, and the third
# entry is empty. Keys count down from ENG-100, so index i is ENG-(100-i).
EXPECTED_UNASSIGNED = [f"ENG-{100 - i}" for i in range(100) if i % 3 == 2]


def root() -> pathlib.Path:
    return pathlib.Path(__file__).resolve().parent.parent


def dump(into: pathlib.Path) -> None:
    """Render every payload in every format into a directory."""
    result = subprocess.run(
        ["go", "test", PACKAGE, "-count=1", "-run", TESTS, f"-cost-dump={into}"],
        cwd=root(),
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        sys.exit(f"go test failed:\n{result.stdout}{result.stderr}")


def token_table(client: anthropic.Anthropic, into: pathlib.Path, model: str) -> None:
    """Count what each payload costs the model that reads it."""
    for stem, command, rows in PAYLOADS:
        print(f"\n{command}   ({rows} row{'s' if rows != 1 else ''})\n")
        print(f"{'format':<6} {'bytes':>7} {'tokens':>8} {'vs tsv':>7} {'per row':>8}")

        base = None
        for fmt in FORMATS:
            text = (into / f"{stem}.{fmt}").read_text(encoding="utf-8")
            counted = client.messages.count_tokens(
                model=model,
                messages=[{"role": "user", "content": text}],
            ).input_tokens
            base = base or counted
            print(f"{fmt:<6} {len(text.encode()):7d} {counted:8d} "
                  f"{f'{counted / base:.2f}x':>7} {counted / rows:8.1f}")

    print(f"\nCounted by {model} via /v1/messages/count_tokens — Claude's own "
          "tokenizer,\nnot a proxy. Counts differ between model families; the "
          "ratio is a property\nof the framing and moves far less than the "
          "absolute numbers do.")


def ask(client: anthropic.Anthropic, model: str, payload: str, nonce: int):
    """Send one payload and time how long the first token takes to arrive.

    The nonce leads the prompt so no two requests share a cacheable prefix. A
    cache hit would make the second measurement of a format a measurement of
    the cache, and the whole point is what it costs to read the payload cold.
    """
    prompt = f"[request {nonce}]\n\n{TASK}\n\n{payload}"
    started = time.perf_counter()
    first = None
    with client.messages.stream(
        model=model,
        max_tokens=1024,
        # Timing how long it takes to read the payload, not to decide what to
        # say about it. Thinking is allowed at effort high or below on Opus 5.
        thinking={"type": "disabled"},
        output_config={"effort": "low"},
        messages=[{"role": "user", "content": prompt}],
    ) as stream:
        for _ in stream.text_stream:
            if first is None:
                first = time.perf_counter() - started
        message = stream.get_final_message()
    total = time.perf_counter() - started

    if message.stop_reason == "refusal":
        sys.exit("the model declined the request; nothing to measure")
    text = "".join(b.text for b in message.content if b.type == "text")
    return first or total, total, message.usage, text


def score(answer: str) -> tuple[int, int]:
    """Compare the model's answer against the keys that are really unassigned."""
    found = set(re.findall(r"ENG-\d+", answer))
    want = set(EXPECTED_UNASSIGNED)
    return len(found & want), len(found - want)


def latency_table(client: anthropic.Anthropic, into: pathlib.Path,
                  model: str, reps: int) -> None:
    """Time a real answer over each format, and check it was right."""
    print(f"\nanswering a question over issue list --limit 100   "
          f"({reps} reps, {model}, thinking off, effort low)\n")
    print(f"{'format':<6} {'in tok':>7} {'TTFT p50':>9} {'vs tsv':>7} "
          f"{'total p50':>10} {'correct':>9} {'wrong':>6}")

    base = None
    for fmt in FORMATS:
        payload = (into / f"issues-100.{fmt}").read_text(encoding="utf-8")
        firsts, totals, hits, misses, tokens = [], [], [], [], 0
        for rep in range(reps):
            first, total, usage, text = ask(client, model, payload, rep)
            firsts.append(first)
            totals.append(total)
            tokens = usage.input_tokens
            hit, miss = score(text)
            hits.append(hit)
            misses.append(miss)

        ttft = statistics.median(firsts)
        base = base or ttft
        print(f"{fmt:<6} {tokens:7d} {f'{ttft:.2f}s':>9} {f'{ttft / base:.2f}x':>7} "
              f"{f'{statistics.median(totals):.2f}s':>10} "
              f"{f'{statistics.median(hits):.0f}/{len(EXPECTED_UNASSIGNED)}':>9} "
              f"{statistics.median(misses):6.0f}")

    print("\nTTFT is dominated by prefill, which is the part the payload size "
          "controls.\nMedian of the reps; one machine, one network, one moment "
          "— the ratio travels\nand the seconds do not. `correct` and `wrong` "
          "are there so a format cannot\nlook fast by answering badly.")


# `BenchmarkParseCost/xml-10  3290  727463 ns/op  48.15 MB/s  345060 B/op  9257 allocs/op`
BENCH_LINE = re.compile(
    r"^Benchmark\w+/(\w+)\S*\s+\d+\s+"
    r"([\d.]+) ns/op\s+([\d.]+) MB/s\s+(\d+) B/op\s+(\d+) allocs/op"
)


def parse_table() -> None:
    """Run the parse benchmark and print what a consumer pays to read each format."""
    result = subprocess.run(
        ["go", "test", PACKAGE, "-run", "^$", "-bench", BENCHMARK,
         "-benchmem", f"-benchtime={BENCHTIME}"],
        cwd=root(),
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
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default="claude-opus-5")
    parser.add_argument("--reps", type=int, default=5,
                        help="latency samples per format (default 5)")
    parser.add_argument("--skip-latency", action="store_true",
                        help="count tokens and parse only; makes no billed calls")
    args = parser.parse_args()

    client = anthropic.Anthropic()
    with tempfile.TemporaryDirectory() as tmp:
        into = pathlib.Path(tmp)
        dump(into)
        token_table(client, into, args.model)
        if not args.skip_latency:
            latency_table(client, into, args.model, args.reps)
    parse_table()


if __name__ == "__main__":
    main()
