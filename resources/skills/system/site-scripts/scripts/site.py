#!/usr/bin/env python3
"""Run Tap-format site scripts through the Lightpanda CLI.

Usage:
  site.py list [--json]
  site.py info <site/name>
  site.py run <site/name> [key=value ...] [--timeout SECONDS] [--raw]
  site.py add <site/name | url | file.js> [--name site/name]

A site script is `<site>/<name>.js`: a `/* @meta {...} */` header with
description, domain, args, readOnly, and optional headers, followed by an
`async function(args)` that runs inside a browser page and returns JSON.
A few scripts ship with the skill; `add` installs more from the Tap catalog,
a URL, or a file into $XDG_CACHE_HOME/site-scripts, where they shadow bundled
scripts of the same name.
"""

import argparse
import collections
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

BUNDLED_DIR = Path(__file__).resolve().parent.parent / "sites"
CATALOG_URL = "https://tap.vaayne.com/api/scripts/{name}/content"
NAME_RE = re.compile(r"^[a-z0-9_-]+/[a-z0-9_-]+$")
META_RE = re.compile(r"\s*/\*\s*@meta\s*(\{.*?\})\s*\*/\s*(async\s+function\b.*)", re.S)
ENV_REF_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")
DEFAULT_TIMEOUT = 60


class SiteError(Exception):
    pass


def user_dir():
    """Where installed and hand-written scripts live: $XDG_CACHE_HOME/site-scripts.

    In the sandbox that is the principal's shared cache, so a script added by one
    agent is visible to every agent of the same user and survives sessions.
    """
    base = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / ".cache")
    return Path(base) / "site-scripts"


def load_catalog():
    """Return {name: (path, meta, body)}; a user script shadows a bundled one of the same name."""
    catalog = {}
    for base in (user_dir(), BUNDLED_DIR):
        if not base.is_dir():
            continue
        for path in sorted(base.glob("*/*.js")):
            name = f"{path.parent.name}/{path.stem}"
            if name in catalog:
                continue
            try:
                catalog[name] = (path,) + parse_script(path.read_text(encoding="utf-8"))
            except SiteError as exc:
                print(f"skipping {path}: {exc}", file=sys.stderr)
    return catalog


def parse_script(source):
    match = META_RE.match(source)
    if not match:
        raise SiteError("missing /* @meta */ header or async function body")
    try:
        meta = json.loads(match.group(1))
    except json.JSONDecodeError as exc:
        raise SiteError(f"invalid @meta JSON: {exc}") from exc
    if not isinstance(meta.get("domain"), str) or not meta["domain"]:
        raise SiteError("@meta.domain is required")
    return meta, match.group(2)


def resolve_headers(meta):
    """Expand ${VAR} references from the environment; drop headers whose variable is unset."""
    headers = {}
    for key, template in (meta.get("headers") or {}).items():
        missing = [var for var in ENV_REF_RE.findall(template) if not os.environ.get(var)]
        if missing:
            continue
        headers[key] = ENV_REF_RE.sub(lambda m: os.environ[m.group(1)], template)
    return headers


def parse_args_kv(pairs):
    args = {}
    for pair in pairs:
        key, sep, value = pair.partition("=")
        if not sep or not key:
            raise SiteError(f"argument {pair!r} must be key=value")
        args[key] = value
    return args


def check_required(meta, args):
    missing = [
        key
        for key, spec in (meta.get("args") or {}).items()
        if isinstance(spec, dict) and spec.get("required") and key not in args
    ]
    if missing:
        raise SiteError("missing required args: " + ", ".join(sorted(missing)))


def page_program(body, args, headers, domain):
    """The JavaScript evaluated inside the page: Tap's fetch wrapper around the script body.

    Headers are attached only to requests whose origin is the declared domain, so
    a credential never travels to a redirect target or a third-party host.
    """
    return f"""(async () => {{
  const __args = {json.dumps(args)};
  const __headers = {json.dumps(headers)};
  const __origin = "https://" + {json.dumps(domain)};
  const __nativeFetch = globalThis.fetch.bind(globalThis);
  const fetch = (input, init = {{}}) => {{
    const url = new URL(input instanceof Request ? input.url : String(input), location.href);
    const headers = new Headers(input instanceof Request ? input.headers : undefined);
    new Headers(init.headers || {{}}).forEach((value, name) => headers.set(name, value));
    if (url.origin === __origin) {{
      for (const [name, value] of Object.entries(__headers)) headers.set(name, value);
    }}
    return __nativeFetch(input, {{...init, headers}});
  }};
  const result = await ({body})(__args);
  return JSON.stringify(result === undefined ? null : result);
}})()"""


NAVIGATE_TIMEOUT_MS = 10000
# Printed right before the result line so the runner can stop the browser as
# soon as the script has answered: Lightpanda otherwise keeps the process alive
# until every request the visited page started has finished or hit --http-timeout.
RESULT_SENTINEL = "<<site-script-result>>"


def panda_script(program, navigate_url):
    """The PandaScript `lightpanda run` executes: open a page, evaluate the program, return its JSON.

    Navigation gets its own short budget: a heavy site root that never reaches
    domcontentloaded must not consume the whole run timeout before the script runs.
    """
    return (
        "const page = new Page();\n"
        f"await page.goto({json.dumps(navigate_url)}, {{ waitUntil: \"domcontentloaded\", timeout: {NAVIGATE_TIMEOUT_MS} }});\n"
        f"const result = await page.evaluate({json.dumps(program)});\n"
        f"console.log({json.dumps(RESULT_SENTINEL)});\n"
        "return result;\n"
    )


def lightpanda_binary():
    binary = os.environ.get("LIGHTPANDA_BIN") or shutil.which("lightpanda")
    if not binary:
        raise SiteError(
            "lightpanda is not on PATH. The Lightpanda manifest tool installs it in the "
            "background after startup; retry shortly or ask an admin to enable tool/lightpanda."
        )
    return binary


RunOutcome = collections.namedtuple("RunOutcome", "returncode result_line stderr")


def run_lightpanda(binary, program, navigate_url, timeout):
    """Run the PandaScript and return the JSON line it produced.

    stdout is read line by line: the line after RESULT_SENTINEL is the answer,
    and the browser is terminated at that point instead of waiting for the
    page's background requests to drain. A process that exits on its own
    (navigation failure, script exception) reports its exit code instead.
    """
    with tempfile.NamedTemporaryFile("w", suffix=".js", prefix="site-", delete=False) as handle:
        handle.write(panda_script(program, navigate_url))
        script_path = handle.name
    cmd = [binary, "run", "--block-private-networks", "--http-timeout", str(timeout * 1000), script_path]
    deadline = time.monotonic() + timeout
    # stderr goes to a file, not a pipe: stdout is read first, and a chatty
    # browser log would otherwise fill the stderr pipe and stall the process.
    stderr_file = tempfile.TemporaryFile("w+")
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=stderr_file, text=True)
    result_line = None
    last_line = None
    try:
        # A result must arrive before the deadline; readline blocks, so the
        # deadline is enforced by the watchdog thread below.
        watchdog = threading.Timer(max(deadline - time.monotonic(), 0), proc.kill)
        watchdog.start()
        try:
            expecting_result = False
            for line in proc.stdout:
                line = line.rstrip("\n")
                if expecting_result:
                    result_line = line
                    break
                if line == RESULT_SENTINEL:
                    expecting_result = True
                elif line.strip():
                    last_line = line
        finally:
            watchdog.cancel()
        if result_line is not None:
            # Nothing to flush: the temp script is ours and the answer is in hand.
            proc.kill()
        timed_out = time.monotonic() >= deadline and result_line is None
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        if timed_out:
            raise subprocess.TimeoutExpired(cmd, timeout)
        stderr_file.seek(0)
        stderr = stderr_file.read()
    finally:
        proc.stdout.close()
        stderr_file.close()
        os.unlink(script_path)
    if result_line is None and proc.returncode == 0:
        # No sentinel (for example a stand-in binary): the last line is the answer.
        result_line = last_line
    return RunOutcome(0 if result_line is not None else proc.returncode, result_line, stderr)


def stderr_detail(stderr):
    """Pick the lines of Lightpanda's stderr that explain a failed run.

    Lightpanda logs fatal errors at level=fatal and a mise shim that cannot
    resolve the binary prints "mise ERROR", so matching only level=error would
    swallow both and report "no output". Fall back to the last few lines so the
    real reason always reaches the caller.
    """
    lines = [line for line in stderr.splitlines() if line.strip()]
    marked = [line for line in lines if "level=error" in line or "level=fatal" in line or "Error" in line or "ERROR" in line]
    picked = marked or lines[-5:]
    return "\n".join(picked).strip() or "no output"


def run_script(name, catalog, pairs, timeout):
    if name not in catalog:
        raise SiteError(f"unknown script {name!r}; run `site.py list`, or `site.py add <site/name>` to install it from the catalog")
    _, meta, body = catalog[name]
    if meta.get("authRequired"):
        raise SiteError(f"{name} needs a logged-in browser session, which Lightpanda does not provide")
    args = parse_args_kv(pairs)
    check_required(meta, args)
    program = page_program(body, args, resolve_headers(meta), meta["domain"])
    binary = lightpanda_binary()

    # Navigate to the declared domain first so same-origin requests carry its
    # cookies; a site whose root redirects in a loop still works from about:blank
    # because Lightpanda does not enforce CORS on fetch().
    last = None
    for navigate_url in (f"https://{meta['domain']}/", "about:blank"):
        try:
            last = run_lightpanda(binary, program, navigate_url, timeout)
        except subprocess.TimeoutExpired as exc:
            raise SiteError(f"{name} exceeded {timeout}s") from exc
        if last.result_line is not None:
            break
    if last.result_line is None:
        raise SiteError(f"lightpanda exited {last.returncode}: {stderr_detail(last.stderr)}")
    # The program returns JSON.stringify(result), which `run` prints verbatim.
    try:
        result = json.loads(last.result_line)
    except json.JSONDecodeError as exc:
        raise SiteError(f"{name} returned non-JSON output: {last.result_line[:300]!r}") from exc
    # Catalog scripts wrap their payload in a versioned envelope; only the data matters here.
    if isinstance(result, dict) and "__pinix_site_result" in result and "data" in result:
        return result["data"]
    return result


def fetch_text(url):
    import urllib.error
    import urllib.request

    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "stella-site-scripts"}), timeout=30) as resp:
            return resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        raise SiteError(f"{url} answered HTTP {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, UnicodeDecodeError) as exc:
        raise SiteError(f"cannot fetch {url}: {exc}") from exc


def add_script(source, name=None):
    """Install a script from a catalog name, a URL, or a local file into user_dir().

    Returns (name, path). The catalog is the Tap site-script index; a name like
    `bilibili/ranking` is fetched from it, so the small bundled set is a floor,
    not a ceiling.
    """
    if source.startswith(("http://", "https://")):
        text, default_name = fetch_text(source), None
    elif NAME_RE.match(source) and not Path(source).exists():
        text, default_name = fetch_text(CATALOG_URL.format(name=source)), source
    else:
        path = Path(source)
        if not path.is_file():
            raise SiteError(f"{source!r} is not a catalog name, URL, or existing file")
        text, default_name = path.read_text(encoding="utf-8"), f"{path.resolve().parent.name}/{path.stem}"
    meta, _ = parse_script(text)
    name = name or meta.get("name") or default_name
    if not name or not NAME_RE.match(name):
        raise SiteError("cannot infer a site/name for this script; pass --name <site>/<name>")
    if meta.get("authRequired"):
        print(f"warning: {name} declares authRequired and will be refused at run time (no login session)", file=sys.stderr)
    target = user_dir() / f"{name}.js"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(text, encoding="utf-8")
    return name, target


def describe(name, meta):
    args = meta.get("args") or {}
    parts = []
    for key, spec in args.items():
        required = isinstance(spec, dict) and spec.get("required")
        parts.append(key if required else f"[{key}]")
    return f"{name:<34} {meta['domain']:<24} {' '.join(parts)}\n    {meta.get('description', '').strip()}"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    list_cmd = sub.add_parser("list", help="list available scripts")
    list_cmd.add_argument("--json", action="store_true", help="print the catalog as JSON")
    info_cmd = sub.add_parser("info", help="print one script's metadata")
    info_cmd.add_argument("name")
    run_cmd = sub.add_parser("run", help="run a script")
    run_cmd.add_argument("name")
    run_cmd.add_argument("pairs", nargs="*", metavar="key=value")
    run_cmd.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT, help="seconds before the run is killed")
    run_cmd.add_argument("--raw", action="store_true", help="print compact JSON instead of indented")
    add_cmd = sub.add_parser("add", help="install a script from the catalog, a URL, or a file")
    add_cmd.add_argument("source", help="<site>/<name> from the catalog, a URL, or a local .js path")
    add_cmd.add_argument("--name", help="<site>/<name> to install as (default: from @meta.name or the source)")
    opts = parser.parse_args(argv)

    try:
        if opts.command == "add":
            name, path = add_script(opts.source, opts.name)
            print(f"installed {name} -> {path}")
            return 0
        catalog = load_catalog()
        if opts.command == "list":
            if opts.json:
                print(json.dumps({name: meta for name, (_, meta, _) in catalog.items()}, ensure_ascii=False, indent=2))
            else:
                for name, (_, meta, _) in catalog.items():
                    print(describe(name, meta))
            return 0
        if opts.command == "info":
            if opts.name not in catalog:
                raise SiteError(f"unknown script {opts.name!r}; run `site.py list`, or `site.py add <site/name>` to install it from the catalog")
            path, meta, _ = catalog[opts.name]
            print(json.dumps({**meta, "name": opts.name, "path": str(path)}, ensure_ascii=False, indent=2))
            return 0
        result = run_script(opts.name, catalog, opts.pairs, opts.timeout)
        print(json.dumps(result, ensure_ascii=False, indent=None if opts.raw else 2))
        return 1 if isinstance(result, dict) and "error" in result else 0
    except SiteError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
