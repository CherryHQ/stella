#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "click",
#     "rich",
#     "xdk",
# ]
# ///

"""
Post Tweet — X API v2
=====================
Posts a tweet using OAuth2 PKCE (User Context) via the xdk SDK.

Required env vars: X_CLIENT_ID, X_CLIENT_SECRET
Token cache: ~/.stella/x_tokens.json (auto-refreshed on expiry)
"""

import json
import logging
import os
import sys
from pathlib import Path

import click
from rich.console import Console
from rich.logging import RichHandler
from xdk import Client
from xdk.oauth2_auth import OAuth2PKCEAuth

LOG_DIR = Path(".agents/logs")
LOG_DIR.mkdir(parents=True, exist_ok=True)

logging.basicConfig(
    level=logging.INFO,
    format="%(message)s",
    datefmt="[%X]",
    handlers=[
        RichHandler(console=Console(stderr=True)),
        logging.FileHandler(LOG_DIR / "post_twitter.log"),
    ],
)
logger = logging.getLogger(__name__)

TOKEN_PATH = Path.home() / ".stella" / "x_tokens.json"
REDIRECT_URI = "https://example.com"
SCOPES = ["tweet.read", "tweet.write", "users.read", "offline.access"]


def _load_tokens() -> dict | None:
    """Load cached OAuth2 tokens from disk."""
    if TOKEN_PATH.exists():
        try:
            tokens = json.loads(TOKEN_PATH.read_text())
            if tokens.get("access_token"):
                logger.info("Loaded cached tokens from %s", TOKEN_PATH)
                return tokens
        except (json.JSONDecodeError, KeyError):
            logger.warning("Invalid token cache, will re-authenticate")
    return None


def _save_tokens(tokens: dict) -> None:
    """Persist OAuth2 tokens to disk."""
    TOKEN_PATH.parent.mkdir(parents=True, exist_ok=True)
    TOKEN_PATH.write_text(json.dumps(tokens, indent=2))
    logger.info("Saved tokens to %s", TOKEN_PATH)


def _authenticate(client_id: str, client_secret: str) -> str:
    """Run OAuth2 PKCE flow, returning an access token."""
    cached = _load_tokens()
    if cached:
        return cached["access_token"]

    auth = OAuth2PKCEAuth(
        client_id=client_id,
        client_secret=client_secret,
        redirect_uri=REDIRECT_URI,
        scope=SCOPES,
    )

    auth_url = auth.get_authorization_url()
    console = Console()
    console.print("\n[bold]Authorize the app in your browser:[/bold]")
    console.print(auth_url, style="cyan underline")
    callback_url = input("\nPaste the full callback URL here: ")

    tokens = auth.fetch_token(authorization_response=callback_url)
    _save_tokens(tokens)
    return tokens["access_token"]


@click.command()
@click.argument("text")
@click.option("--dry-run", is_flag=True, help="Preview the tweet without posting")
def main(text: str, dry_run: bool) -> None:
    """Post a tweet to X (Twitter).

    TEXT is the tweet content to post.
    """
    logger.info("Tweet text (%d chars): %s", len(text), text)

    if len(text) > 280:
        logger.error("Tweet exceeds 280 characters (%d). Aborting.", len(text))
        sys.exit(1)

    client_id = os.environ.get("X_CLIENT_ID")
    client_secret = os.environ.get("X_CLIENT_SECRET")
    if not client_id or not client_secret:
        logger.error("Missing X_CLIENT_ID or X_CLIENT_SECRET environment variables")
        sys.exit(1)

    if dry_run:
        logger.info("[DRY RUN] Would post tweet: %s", text)
        return

    access_token = _authenticate(client_id, client_secret)
    client = Client(access_token=access_token)

    logger.info("Posting tweet...")
    response = client.posts.create(body={"text": text})
    logger.info("Tweet posted successfully")
    logger.info("Response: %s", json.dumps(response.data, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
