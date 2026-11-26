"""
Optional browser-based session token extraction via Playwright.

This module allows extracting the Firebase authentication token directly from
a Chrome/Brave browser via the Chrome DevTools Protocol (CDP) or by resolving
exported session cookies in a lightweight headless browser context.

Usage
-----
Run with ``--browser`` to auto-launch Chrome, sign in to KodeKloud, and
extract the session token:

    kodekloud dl --browser -o . "https://kodekloud.com/courses/..."

Or start Chrome manually with remote debugging, sign in, then run:

    chrome.exe --remote-debugging-port=9222
    kodekloud dl --browser -o . "https://..."
"""

from __future__ import annotations

import logging
import os
import platform
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any, Optional

logger = logging.getLogger(__name__)

BROWSER_AUTH_ENABLED = os.environ.get("KODEKLOUD_USE_BROWSER", "").lower() in (
    "1",
    "true",
    "yes",
)

CDP_PORT = int(os.environ.get("KODEKLOUD_CDP_PORT", "9222"))


def _import_playwright():
    """Lazy-import Playwright; return None if not installed."""
    try:
        from playwright.sync_api import sync_playwright as _sp

        return _sp
    except ImportError:
        return None


def _chrome_default_path() -> Optional[Path]:
    """Return the default browser executable path for the current platform."""
    system = platform.system()
    if system == "Windows":
        candidates = [
            Path(os.environ.get("LOCALAPPDATA", ""))
            / "Google"
            / "Chrome"
            / "Application"
            / "chrome.exe",
            Path(os.environ.get("PROGRAMFILES", ""))
            / "Google"
            / "Chrome"
            / "Application"
            / "chrome.exe",
            Path(os.environ.get("PROGRAMFILES(X86)", ""))
            / "Google"
            / "Chrome"
            / "Application"
            / "chrome.exe",
            Path(os.environ.get("LOCALAPPDATA", ""))
            / "BraveSoftware"
            / "Brave-Browser"
            / "Application"
            / "brave.exe",
            Path(os.environ.get("PROGRAMFILES", ""))
            / "BraveSoftware"
            / "Brave-Browser"
            / "Application"
            / "brave.exe",
        ]
    elif system == "Darwin":
        candidates = [
            Path("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
            Path("/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"),
            Path("/Applications/Chromium.app/Contents/MacOS/Chromium"),
        ]
    elif system == "Linux":
        candidates = [
            Path("/usr/bin/google-chrome"),
            Path("/usr/bin/chromium"),
            Path("/usr/bin/chromium-browser"),
            Path("/usr/bin/brave-browser"),
        ]
    else:
        return None

    for path in candidates:
        if path.exists():
            return path
    return None


def _launch_chrome_with_debugging(port: int) -> Optional[subprocess.Popen]:
    """Launch Chrome with remote debugging enabled and a temporary profile.

    Returns the process handle or None on failure.
    """
    chrome_path = _chrome_default_path()
    if chrome_path is None:
        return None

    user_data_dir = Path(tempfile.gettempdir()) / "kodekloud-chrome-profile"
    user_data_dir.mkdir(parents=True, exist_ok=True)

    try:
        proc = subprocess.Popen(
            [
                str(chrome_path),
                f"--remote-debugging-port={port}",
                f"--user-data-dir={user_data_dir}",
                "--no-first-run",
                "--no-default-browser-check",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        return proc
    except (OSError, subprocess.SubprocessError):
        return None


def parse_netscape_cookies(cookiefile: str | Path) -> list[dict]:
    """Parse a Netscape format cookie file into a list of dicts suitable for Playwright.

    Only kodekloud.com cookies are returned, so an "export all cookies" file
    never hands unrelated sessions to the headless browser.

    :param cookiefile: Path to the cookie file.
    :return: List of cookie dictionaries for context.add_cookies().
    """
    cookies = []
    with open(cookiefile) as fp:
        for line in fp:
            line_str = line.strip()
            # curl/yt-dlp style exports mark HttpOnly cookies with this prefix
            if line_str.startswith("#HttpOnly_"):
                line_str = line_str[len("#HttpOnly_") :]
            if line_str and not line_str.startswith("#"):
                parts = line_str.split("\t")
                if len(parts) > 6:
                    domain, _flag, path, secure, expiration, name, value = parts[:7]
                    host = domain.lstrip(".").lower()
                    if host != "kodekloud.com" and not host.endswith(".kodekloud.com"):
                        continue
                    cookie_dict = {
                        "domain": domain,
                        "path": path,
                        "name": name,
                        "value": value,
                        "secure": secure.lower() == "true",
                    }
                    try:
                        exp = float(expiration)
                        if exp > 0:
                            cookie_dict["expires"] = exp
                    except (ValueError, TypeError):
                        pass
                    cookies.append(cookie_dict)
    return cookies


def _extract_firebase_tokens_from_page(page) -> Optional[dict]:
    """Read the signed-in Firebase user from IndexedDB.

    Returns ``{"id_token", "refresh_token", "expires_at", "api_key"}`` or None.
    The refresh token is what lets the downloader renew the ID token on its own
    for the rest of the run (and later runs) without a browser.
    """
    try:
        data = page.evaluate("""async () => {
            return new Promise((resolve) => {
                const req = indexedDB.open('firebaseLocalStorageDb');
                req.onsuccess = (e) => {
                    const db = e.target.result;
                    if (!db.objectStoreNames.contains('firebaseLocalStorage')) {
                        resolve(null);
                        return;
                    }
                    const tx = db.transaction('firebaseLocalStorage', 'readonly');
                    const store = tx.objectStore('firebaseLocalStorage');
                    const getAll = store.getAll();
                    getAll.onsuccess = () => {
                        for (const row of (getAll.result || [])) {
                            const val = row && row.value;
                            const sts = val && val.stsTokenManager;
                            if (sts && sts.accessToken) {
                                resolve({
                                    id_token: sts.accessToken,
                                    refresh_token: sts.refreshToken || null,
                                    expiration_ms: sts.expirationTime || null,
                                    api_key: val.apiKey || null,
                                });
                                return;
                            }
                        }
                        resolve(null);
                    };
                    getAll.onerror = () => resolve(null);
                };
                req.onerror = () => resolve(null);
            });
        }""")
        if (
            isinstance(data, dict)
            and isinstance(data.get("id_token"), str)
            and data["id_token"].startswith("ey")
        ):
            exp_ms = data.pop("expiration_ms", None)
            if exp_ms:
                data["expires_at"] = float(exp_ms) / 1000.0
            return data
    except Exception as e:
        logger.debug("IndexedDB token extraction attempt: %s", e)
    return None


def _extract_firebase_token_from_page(page) -> Optional[str]:
    """Extract the Firebase JWT accessToken from IndexedDB in the page."""
    tokens = _extract_firebase_tokens_from_page(page)
    return tokens["id_token"] if tokens else None


def _extract_session_cookie(context) -> Optional[str]:
    """Extract any legacy session-cookie from the Playwright browser context."""
    for cookie in context.cookies():
        if cookie["name"] == "session-cookie":
            return cookie["value"]
    return None


def get_token_from_cookie_file(cookiefile: str | Path) -> Optional[str]:
    """Exchange exported cookies for a Firebase ID token (see get_tokens_...)."""
    tokens = get_tokens_from_cookie_file(cookiefile)
    return tokens["id_token"] if tokens else None


def get_tokens_from_cookie_file(cookiefile: str | Path) -> Optional[dict]:
    """Exchange exported cookies (e.g. _secure-user-session) for Firebase tokens.

    Launches a lightweight headless browser, loads the cookies, navigates to
    KodeKloud, and extracts the Firebase tokens from IndexedDB.

    :param cookiefile: Path to the cookie file.
    :return: ``{"id_token", "refresh_token", "expires_at", "api_key"}`` (the
        last three may be missing) if resolved, otherwise None.
    """
    sp = _import_playwright()
    if sp is None:
        logger.error("Playwright is not installed.")
        return None

    cookies = parse_netscape_cookies(cookiefile)
    if not cookies:
        return None

    chrome_path = _chrome_default_path()
    with sp() as pw:
        launch_kwargs: dict[str, Any] = {"headless": True}
        if chrome_path is not None:
            launch_kwargs["executable_path"] = str(chrome_path)

        try:
            browser = pw.chromium.launch(**launch_kwargs)
        except Exception as e:
            logger.error("Could not launch browser for cookie exchange: %s", e)
            return None

        context = browser.new_context()
        try:
            context.add_cookies(cookies)
        except Exception as e:
            logger.debug("Warning setting cookies: %s", e)

        page = context.new_page()
        intercepted_token: Optional[str] = None

        def _handle_request(request):
            nonlocal intercepted_token
            if "learn-api.kodekloud.com" in request.url:
                auth = request.headers.get("authorization", "")
                if auth.startswith("Bearer ey"):
                    intercepted_token = auth.split(" ", 1)[1]

        page.on("request", _handle_request)

        try:
            page.goto(
                "https://learn.kodekloud.com/learn/courses",
                wait_until="networkidle",
                timeout=30000,
            )
        except Exception as e:
            logger.debug("Page navigation: %s", e)

        time.sleep(2)
        tokens = _extract_firebase_tokens_from_page(page)
        browser.close()
        if tokens:
            return tokens
        return {"id_token": intercepted_token} if intercepted_token else None


def get_session_token_from_browser(
    port: int = CDP_PORT,
    auto_launch: bool = False,
) -> Optional[str]:
    """Like :func:`get_session_tokens_from_browser` but returns only the ID token."""
    tokens = get_session_tokens_from_browser(port=port, auto_launch=auto_launch)
    return tokens["id_token"] if tokens else None


def get_session_tokens_from_browser(
    port: int = CDP_PORT,
    auto_launch: bool = False,
) -> Optional[dict]:
    """Extract the authentication token from a Chrome browser via CDP.

    Tries connecting to a running Chrome instance first. If that fails
    and ``auto_launch`` is enabled, starts a new Chrome with remote
    debugging and guides the user through sign-in.

    Parameters
    ----------
    port:
        The CDP port to connect to.
    auto_launch:
        If True, launch Chrome with remote debugging if no running
        instance is detected.

    Returns
    -------
    ``{"id_token", "refresh_token"?, "expires_at"?, "api_key"?}`` or None.
    """
    sp = _import_playwright()
    if sp is None:
        print(
            "Playwright is not installed. To use browser-based auth:\n"
            "  pip install kodekloud-downloader[browser]"
        )
        return None

    chrome_proc = None
    browser = None

    with sp() as pw:
        # --- Step 1: try connecting to a running Chrome ---
        try:
            browser = pw.chromium.connect_over_cdp(f"http://127.0.0.1:{port}")
            logger.info("Connected to running Chrome on port %d", port)
        except Exception:
            pass

        # --- Step 2: auto-launch if needed ---
        if browser is None and auto_launch:
            auto_port = (
                port + 1
                if port == int(os.environ.get("KODEKLOUD_CDP_PORT", "9222"))
                else port
            )
            logger.info(
                "Launching Chrome with remote debugging on port %d...", auto_port
            )
            chrome_proc = _launch_chrome_with_debugging(auto_port)
            if chrome_proc is not None:
                for _ in range(5):  # retry up to 5 times (~10s)
                    time.sleep(2)
                    try:
                        browser = pw.chromium.connect_over_cdp(
                            f"http://127.0.0.1:{auto_port}"
                        )
                        logger.info("Chrome launched and connected")
                        break
                    except Exception:
                        logger.debug("Waiting for Chrome CDP on port %d...", auto_port)
                        continue

        if browser is None:
            print(
                "Could not connect to Chrome. Please start Chrome with:\n"
                f"  chrome.exe --remote-debugging-port={port}\n"
                "Then sign in to https://learn.kodekloud.com and try again."
            )
            return None

        # --- Step 3: inspect pages and navigate ---
        try:
            context = browser.contexts[0]
        except IndexError:
            browser.close()
            return None

        page = context.pages[0] if context.pages else context.new_page()

        # Listen for API calls carrying Bearer tokens
        intercepted_token: Optional[str] = None

        def _handle_request(request):
            nonlocal intercepted_token
            if "learn-api.kodekloud.com" in request.url:
                auth = request.headers.get("authorization", "")
                if auth.startswith("Bearer ey"):
                    intercepted_token = auth.split(" ", 1)[1]

        page.on("request", _handle_request)

        def _collect() -> Optional[dict]:
            tokens = _extract_firebase_tokens_from_page(page)
            if tokens:
                return tokens
            legacy = _extract_session_cookie(context) or intercepted_token
            return {"id_token": legacy} if legacy else None

        # Check existing session in page / cookies
        token = _collect()
        if token:
            logger.info("Session token found in existing browser state")
            if chrome_proc is not None:
                chrome_proc.terminate()
            else:
                browser.close()
            return token

        # Navigate to KodeKloud
        logger.info("Navigating to KodeKloud...")
        try:
            page.goto(
                "https://learn.kodekloud.com/learn/courses",
                wait_until="networkidle",
                timeout=30000,
            )
        except Exception:
            pass

        time.sleep(2)

        # Check again after navigation
        token = _collect()
        if token:
            logger.info("Session token obtained after navigation")
            if chrome_proc is not None:
                chrome_proc.terminate()
            else:
                browser.close()
            return token

        # --- Step 4: if not logged in, prompt user ---
        current_url = page.url.lower()
        if all(
            word not in current_url for word in ["sign-in", "login", "auth", "signin"]
        ):
            try:
                page.goto(
                    "https://identity.kodekloud.com/sign-in",
                    wait_until="networkidle",
                    timeout=15000,
                )
            except Exception:
                pass

        print(
            "Please sign in to KodeKloud in the opened browser window, "
            "then press Enter here..."
        )
        try:
            input()
        except (EOFError, KeyboardInterrupt):
            pass

        # Wait for the token after user sign in
        logger.info("Waiting for token...")
        for _ in range(15):  # wait up to ~30s
            token = _collect()
            if token:
                break
            time.sleep(2)

        # Cleanup
        if chrome_proc is not None:
            chrome_proc.terminate()
        else:
            browser.close()

        if token:
            logger.info("Session token extracted successfully")
        else:
            print(
                "Could not find session token. Ensure you are signed in to KodeKloud."
            )

        return token
