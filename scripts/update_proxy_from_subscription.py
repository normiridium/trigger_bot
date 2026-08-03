#!/usr/bin/env python3
"""Update local sing-box proxy configs from a subscription profile.

The script reads a v2ray/xray-style JSON subscription, selects country-specific
VLESS TCP+Reality outbounds, converts them with the local proxy converter, and
optionally installs/restarts the affected user services.
"""

from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path
from typing import Any


JSON = dict[str, Any]

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_ENV = ROOT / ".env"
DEFAULT_SINGBOX_DIR = ROOT / ".singbox"
DEFAULT_CONVERTER = Path("/home/faline/.codex/skills/proxy-format-converter/scripts/convert_proxy_config.py")

TARGETS = {
    "vk": {
        "country": "ru",
        "country_env": "PROXY_SUBSCRIPTION_VK_COUNTRY",
        "listen_port": 10808,
        "tag_prefix": "vk",
        "config": "vless-ru.json",
        "service": "sing-box-vless.service",
        "probe_url": "https://vk.ru/",
    },
    "tiktok": {
        "country": "nl",
        "country_env": "PROXY_SUBSCRIPTION_TIKTOK_COUNTRY",
        "listen_port": 10809,
        "tag_prefix": "tiktok",
        "config": "tiktok-vless-ru.json",
        "service": "sing-box-tiktok.service",
        "probe_url": "https://www.tiktok.com/",
    },
}

COUNTRY_ALIASES = {
    "ru": ["🇷🇺", "россия", "russia", "moscow", "москва", " ru", "ru "],
    "nl": ["🇳🇱", "нидерланды", "netherlands", "nederland", "holland", "amsterdam", " nl", "nl "],
}


def log(message: str) -> None:
    print(message, flush=True)


def load_env(path: Path) -> dict[str, str]:
    env: dict[str, str] = {}
    if not path.exists():
        return env
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip().strip('"').strip("'")
        env[key.strip()] = value
    return env


def env_bool(value: str | None, default: bool = False) -> bool:
    if value is None or value == "":
        return default
    return value.strip().lower() in {"1", "true", "yes", "y", "on"}


def run(cmd: list[str], *, cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(cmd, cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
    if check and proc.returncode != 0:
        sys.stderr.write(proc.stdout)
        raise SystemExit(proc.returncode)
    return proc


def fetch_subscription(url: str, timeout: int, user_agent: str) -> Any:
    req = urllib.request.Request(url, headers={"User-Agent": user_agent})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
    text = raw.decode("utf-8", "replace").strip()
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        compact = "".join(text.split())
        try:
            padded = compact + ("=" * ((4 - len(compact) % 4) % 4))
            decoded = base64.b64decode(padded).decode("utf-8", "replace").strip()
            return json.loads(decoded)
        except Exception as exc:  # noqa: BLE001 - report real subscription shape error.
            raise SystemExit(f"subscription is not supported JSON/base64-JSON: {exc}") from exc


def profiles_from_subscription(data: Any) -> list[JSON]:
    if isinstance(data, dict):
        if isinstance(data.get("outbounds"), list):
            return [data]
        for key in ("profiles", "configs", "items"):
            if isinstance(data.get(key), list):
                return [x for x in data[key] if isinstance(x, dict)]
    if isinstance(data, list):
        return [x for x in data if isinstance(x, dict)]
    raise SystemExit("subscription contains no JSON profiles")


def profile_name(profile: JSON) -> str:
    for key in ("remarks", "name", "ps", "tag"):
        value = profile.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return "<unnamed>"


def country_matches(name: str, country: str) -> bool:
    aliases = COUNTRY_ALIASES.get(country.lower(), [country.lower()])
    normalized = f" {name.lower()} "
    return any(alias.lower() in normalized for alias in aliases)


def nested(obj: JSON, path: str) -> Any:
    cur: Any = obj
    for part in path.split("."):
        if isinstance(cur, dict):
            cur = cur.get(part)
        elif isinstance(cur, list):
            try:
                cur = cur[int(part)]
            except (ValueError, IndexError):
                return None
        else:
            return None
    return cur


def compatible_vless_reality(outbound: JSON) -> bool:
    return (
        outbound.get("protocol") == "vless"
        and nested(outbound, "streamSettings.network") == "tcp"
        and nested(outbound, "streamSettings.security") == "reality"
        and isinstance(nested(outbound, "settings.vnext.0"), dict)
        and isinstance(nested(outbound, "settings.vnext.0.users.0"), dict)
    )


def selected_source(profiles: list[JSON], country: str, tag_prefix: str, max_outbounds: int) -> tuple[JSON, list[str]]:
    selected: list[JSON] = []
    matched_profiles: list[str] = []
    for profile in profiles:
        name = profile_name(profile)
        if not country_matches(name, country):
            continue
        profile_had_compatible = False
        for outbound in profile.get("outbounds", []):
            if not isinstance(outbound, dict) or not compatible_vless_reality(outbound):
                continue
            cloned = copy.deepcopy(outbound)
            cloned["tag"] = f"{tag_prefix}-{len(selected) + 1:03d}"
            selected.append(cloned)
            profile_had_compatible = True
            if len(selected) >= max_outbounds:
                break
        if profile_had_compatible:
            matched_profiles.append(name)
        if len(selected) >= max_outbounds:
            break
    if not selected:
        raise SystemExit(f"no compatible VLESS TCP+Reality outbounds found for country={country}")
    return {"outbounds": selected}, matched_profiles


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def convert_target(source: JSON, args: argparse.Namespace, target_name: str, target: JSON, workdir: Path) -> Path:
    source_path = workdir / f"{target_name}-source.json"
    output_path = workdir / f"{target_name}-singbox.json"
    source_path.write_text(json.dumps(source, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    cmd = [
        str(args.converter),
        "--input",
        str(source_path),
        "--output",
        str(output_path),
        "--listen",
        args.listen,
        "--port",
        str(target["listen_port"]),
        "--tag-prefix",
        str(target["tag_prefix"]),
        "--final",
        "auto",
        "--check",
    ]
    proc = run(cmd)
    for line in proc.stdout.splitlines():
        if line.startswith(("listen=", "converted=", "skipped=", "check=")):
            log(f"{target_name}: {line}")
    return output_path


def install_config(candidate: Path, destination: Path, backup_suffix: str) -> Path | None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and sha256(candidate) == sha256(destination):
        log(f"{destination.name}: unchanged")
        return None
    backup: Path | None = None
    if destination.exists():
        backup = destination.with_name(destination.name + f".bak_{backup_suffix}")
        shutil.copy2(destination, backup)
        backup.chmod(0o600)
    shutil.copy2(candidate, destination)
    destination.chmod(0o600)
    log(f"{destination.name}: installed" + (f" backup={backup}" if backup else ""))
    return backup


def restart_user_service(service: str) -> None:
    run(["systemctl", "--user", "restart", service])
    log(f"{service}: restarted")


def probe_proxy(port: int, url: str, attempts: int, timeout: int) -> bool:
    for attempt in range(1, attempts + 1):
        proc = run(
            [
                "curl",
                "-sS",
                "-I",
                "--max-time",
                str(timeout),
                "--proxy",
                f"socks5://127.0.0.1:{port}",
                url,
                "-o",
                "/dev/null",
                "-w",
                "http=%{http_code} total=%{time_total}",
            ],
            check=False,
        )
        line = proc.stdout.strip().splitlines()[-1] if proc.stdout.strip() else ""
        ok = proc.returncode == 0 and "http=000" not in line
        log(f"probe port={port} attempt={attempt}/{attempts} ok={str(ok).lower()} {line}")
        if ok:
            return True
        if attempt < attempts:
            time.sleep(1)
    return False


def restore_backup(destination: Path, backup: Path | None, service: str) -> None:
    if backup is None:
        return
    shutil.copy2(backup, destination)
    destination.chmod(0o600)
    restart_user_service(service)
    log(f"{destination.name}: restored from {backup}")


def main() -> None:
    parser = argparse.ArgumentParser(description="Update VK/TikTok sing-box configs from proxy subscription")
    parser.add_argument("--env", type=Path, default=DEFAULT_ENV)
    parser.add_argument("--subscription-url", default="")
    parser.add_argument("--service", choices=["both", "vk", "tiktok"], default="both")
    parser.add_argument("--singbox-dir", type=Path, default=DEFAULT_SINGBOX_DIR)
    parser.add_argument("--converter", type=Path, default=DEFAULT_CONVERTER)
    parser.add_argument("--listen", default="127.0.0.1")
    parser.add_argument("--max-outbounds", type=int, default=12)
    parser.add_argument("--fetch-timeout", type=int, default=30)
    parser.add_argument("--user-agent", default="ClashforWindows/0.20.39")
    parser.add_argument("--install", action="store_true", help="install generated configs and restart changed services")
    parser.add_argument("--probe", action="store_true", help="probe changed proxies after restart and rollback on failure")
    parser.add_argument("--probe-attempts", type=int, default=3)
    parser.add_argument("--probe-timeout", type=int, default=8)
    parser.add_argument("--settle-sec", type=float, default=1.0, help="delay after restart before probing")
    args = parser.parse_args()

    env = load_env(args.env)
    subscription_url = args.subscription_url or env.get("PROXY_SUBSCRIPTION_URL", "")
    if not subscription_url:
        raise SystemExit("PROXY_SUBSCRIPTION_URL is not configured")
    if not args.converter.exists():
        raise SystemExit(f"converter not found: {args.converter}")

    targets = ["vk", "tiktok"] if args.service == "both" else [args.service]
    profiles = profiles_from_subscription(fetch_subscription(subscription_url, args.fetch_timeout, args.user_agent))
    log(f"profiles={len(profiles)}")

    backup_suffix = time.strftime("%Y%m%d-%H%M%S")
    installed: dict[str, tuple[Path, Path | None]] = {}
    changed_services: list[str] = []

    with tempfile.TemporaryDirectory(prefix="proxy-sub-update-") as tmp:
        workdir = Path(tmp)
        for target_name in targets:
            target = TARGETS[target_name]
            country = env.get(str(target["country_env"]), str(target["country"]))
            source, profile_names = selected_source(profiles, country, str(target["tag_prefix"]), args.max_outbounds)
            log(f"{target_name}: country={country} profiles={len(profile_names)} outbounds={len(source['outbounds'])}")
            for name in profile_names:
                log(f"{target_name}: selected_profile={name}")
            candidate = convert_target(source, args, target_name, target, workdir)
            if not args.install:
                log(f"{target_name}: dry-run candidate={candidate}")
                continue
            destination = args.singbox_dir / str(target["config"])
            backup = install_config(candidate, destination, backup_suffix)
            if backup is not None:
                installed[target_name] = (destination, backup)
                changed_services.append(str(target["service"]))

        for service in changed_services:
            restart_user_service(service)

        if args.install and args.probe:
            if changed_services and args.settle_sec > 0:
                time.sleep(args.settle_sec)
            failures: list[str] = []
            for target_name in targets:
                target = TARGETS[target_name]
                if target_name not in installed:
                    continue
                ok = probe_proxy(int(target["listen_port"]), str(target["probe_url"]), args.probe_attempts, args.probe_timeout)
                if not ok:
                    destination, backup = installed[target_name]
                    restore_backup(destination, backup, str(target["service"]))
                    failures.append(target_name)
            if failures:
                raise SystemExit("probe failed, rolled back: " + ",".join(failures))

    log("done")


if __name__ == "__main__":
    main()
