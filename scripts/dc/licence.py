#!/usr/bin/env python3
"""Print the Data Center timebomb licence this rig runs on.

Atlassian publishes timebomb licences for running a Data Center product without
the SDK. They are the only key an individual can still get: self-serve trial
licences for Atlassian-owned Data Center products ended 30 March 2026, and Data
Center is end-of-life, so there is no purchase path for a new customer either.

The key is fetched rather than vendored, on purpose. It carries "Do not
distribute this to customers", and committing it to a repository is
distributing it. Fetching also makes the day Atlassian withdraws the page a loud
failure naming the URL, instead of a stale copy failing inside a setup wizard.

Nothing is trusted about the page's structure. Every key-shaped string on it is
decoded and the one that is a Jira Software Data Center licence with a
three-hour expiry is selected, so a reworded heading changes nothing and a
substituted key is caught.

Usage:
    licence.py            # fetch, verify, print the key
    licence.py --details  # print the decoded properties to stderr as well

If the fetch fails, save the key by hand as scripts/dc/licence.txt (gitignored)
and every script here will use that instead.
"""

import argparse
import base64
import re
import sys
import urllib.error
import urllib.request
import zlib

PAGE = (
    "https://developer.atlassian.com/platform/marketplace/"
    "timebomb-licenses-for-testing-server-apps/"
)

# A licence is base64 with a short trailing checksum introduced by X0. The
# payload is a 9-byte header followed by a zlib stream.
#
# The page wraps each key across lines, and the wrap is a literal backslash-n
# in the source rather than a newline, so the character class has to admit both
# and the match is stripped back to base64 afterwards. A regex over the plain
# alphabet finds nothing here and reports it as "the page stopped publishing
# keys", which is the wrong failure to hand somebody.
CANDIDATE = re.compile(r"AAAB(?:[A-Za-z0-9+/=]|\\n|\s){100,}")
NOT_BASE64 = re.compile(r"[^A-Za-z0-9+/=]")
SUFFIX = re.compile(r"X0[0-9A-Za-z]{1,4}$")


def decode(candidate: str) -> dict[str, str] | None:
    """Return the licence properties, or None if this is not a licence."""
    body = SUFFIX.sub("", candidate)
    body += "=" * (-len(body) % 4)
    try:
        raw = base64.b64decode(body)
        text = zlib.decompress(raw[9:]).decode("utf-8", "replace")
    except (ValueError, zlib.error):
        return None

    props: dict[str, str] = {}
    for line in text.splitlines():
        if line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        props[key.strip()] = value.strip()
    return props or None


def wanted(props: dict[str, str]) -> bool:
    """Report whether these properties are the licence this rig needs.

    Three conditions, each of which has to hold for the rig to work at all.

    Data Center, because Jira 9.13 and later refuse a Server licence outright.
    Jira Software, because every agile endpoint in the fixture set — board,
    sprint, epic, and the whole of internal/workflow — is served by Jira
    Software and not by Core, and the page publishes a Service Management Data
    Center key beside this one that would satisfy the first condition alone.
    And a relative expiry, which is what makes the key reusable at all: P3H
    runs from install, so a fresh container gets a fresh three hours however
    old the key is.
    """
    if props.get("jira.DataCenter") != "true":
        return False
    if not props.get("LicenseExpiryDate", "").startswith("P"):
        return False
    return any("jira-software" in name for name in props)


def fetch(url: str) -> str:
    request = urllib.request.Request(url, headers={"User-Agent": "jr-fixture-rig"})
    with urllib.request.urlopen(request, timeout=60) as response:  # noqa: S310
        return response.read().decode("utf-8", "replace")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--details", action="store_true",
                        help="print the decoded properties to stderr")
    parser.add_argument("--url", default=PAGE, help="override the source page")
    args = parser.parse_args()

    try:
        page = fetch(args.url)
    except (urllib.error.URLError, TimeoutError) as err:
        print(f"could not fetch {args.url}: {err}", file=sys.stderr)
        print("save the key by hand as scripts/dc/licence.txt and re-run",
              file=sys.stderr)
        return 1

    seen: list[tuple[str, dict[str, str]]] = []
    for match in dict.fromkeys(CANDIDATE.findall(page)):
        candidate = NOT_BASE64.sub("", match)
        props = decode(candidate)
        if props is not None:
            seen.append((candidate, props))

    matches = [(key, props) for key, props in seen if wanted(props)]
    if not matches:
        print(f"{args.url} no longer publishes a Data Center timebomb licence.",
              file=sys.stderr)
        print(f"{len(seen)} key(s) decoded, none of them Data Center with a "
              "relative expiry.", file=sys.stderr)
        print("Read the page, and if the section is gone, this rig has lost its "
              "only licence source — say so rather than working around it.",
              file=sys.stderr)
        return 1

    # Prefer the shortest expiry: the page publishes several, and the shortest
    # is the one whose terms are least likely to be mistaken for an evaluation.
    matches.sort(key=lambda m: m[1].get("LicenseExpiryDate", "P999D"))
    key, props = matches[0]

    if args.details:
        for name in sorted(props):
            print(f"    {name}={props[name]}", file=sys.stderr)

    print(key)
    return 0


if __name__ == "__main__":
    sys.exit(main())
