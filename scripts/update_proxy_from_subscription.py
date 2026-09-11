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
import signal
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:  # pragma: no cover - PyYAML is optional until Clash YAML is used.
    yaml = None


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
        "probe_url": "https://vt.tiktok.com/",
        "probe_follow_redirects": True,
        "filter_outbounds_by_probe": True,
        "prefer_profile_aliases": ["бс-0"],
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


def compact_dict(value: JSON) -> JSON:
    return {k: v for k, v in value.items() if v not in (None, "", [], {})}


def fetch_subscription(url: str, timeout: int, user_agent: str) -> Any:
    req = urllib.request.Request(url, headers={"User-Agent": user_agent})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
    text = raw.decode("utf-8", "replace").strip()
    parsed = parse_subscription_text(text)
    if parsed is not None:
        return parsed

    compact = "".join(text.split())
    try:
        padded = compact + ("=" * ((4 - len(compact) % 4) % 4))
        decoded = base64.b64decode(padded.encode("ascii")).decode("utf-8", "replace").strip()
    except Exception as exc:  # noqa: BLE001 - report real subscription shape error.
        raise SystemExit(f"subscription is not supported JSON/base64-JSON/Clash-YAML: {exc}") from exc

    parsed = parse_subscription_text(decoded)
    if parsed is not None:
        return parsed
    raise SystemExit("subscription is not supported JSON/base64-JSON/Clash-YAML")


def parse_subscription_text(text: str) -> Any | None:
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    if yaml is None:
        return None
    try:
        data = yaml.safe_load(text)
    except Exception:
        return None
    if isinstance(data, (dict, list)):
        return data
    return None


def profiles_from_subscription(data: Any) -> list[JSON]:
    if isinstance(data, dict):
        if isinstance(data.get("proxies"), list):
            profiles: list[JSON] = []
            for proxy_item in data["proxies"]:
                if not isinstance(proxy_item, dict):
                    continue
                outbound = outbound_from_clash_proxy(proxy_item)
                if outbound:
                    profiles.append({"remarks": profile_name(proxy_item), "outbounds": [outbound]})
            if profiles:
                return profiles
        if isinstance(data.get("outbounds"), list):
            return [data]
        for key in ("profiles", "configs", "items"):
            if isinstance(data.get(key), list):
                return [x for x in data[key] if isinstance(x, dict)]
    if isinstance(data, list):
        return [x for x in data if isinstance(x, dict)]
    raise SystemExit("subscription contains no JSON profiles")


def outbound_from_clash_proxy(proxy_item: JSON) -> JSON | None:
    proxy_type = str(proxy_item.get("type") or "").strip().lower()
    if proxy_type == "vless":
        return vless_outbound_from_clash_proxy(proxy_item)
    if proxy_type in {"hysteria2", "hy2"}:
        return hysteria2_outbound_from_clash_proxy(proxy_item)
    return None


def vless_outbound_from_clash_proxy(proxy_item: JSON) -> JSON | None:
    if str(proxy_item.get("network") or "tcp").strip().lower() != "tcp":
        return None
    if not bool(proxy_item.get("tls")):
        return None
    reality = proxy_item.get("reality-opts") or proxy_item.get("reality_opts")
    if not isinstance(reality, dict):
        return None
    server = str(proxy_item.get("server") or "").strip()
    uuid = str(proxy_item.get("uuid") or "").strip()
    public_key = str(reality.get("public-key") or reality.get("public_key") or "").strip()
    if not server or not uuid or not public_key:
        return None
    return {
        "protocol": "vless",
        "tag": profile_name(proxy_item),
        "settings": {
            "vnext": [
                {
                    "address": server,
                    "port": int(proxy_item.get("port") or 443),
                    "users": [
                        {
                            "id": uuid,
                            "encryption": "none",
                            "flow": str(proxy_item.get("flow") or "").strip(),
                        }
                    ],
                }
            ]
        },
        "streamSettings": {
            "network": "tcp",
            "security": "reality",
            "realitySettings": {
                "serverName": str(proxy_item.get("servername") or proxy_item.get("sni") or "").strip(),
                "fingerprint": str(proxy_item.get("client-fingerprint") or proxy_item.get("client_fingerprint") or "firefox").strip(),
                "publicKey": public_key,
                "shortId": str(reality.get("short-id") or reality.get("short_id") or "").strip(),
            },
        },
    }


def hysteria2_outbound_from_clash_proxy(proxy_item: JSON) -> JSON | None:
    server = str(proxy_item.get("server") or "").strip()
    password = str(proxy_item.get("password") or "").strip()
    if not server or not password:
        return None
    return {
        "protocol": "hysteria2",
        "tag": profile_name(proxy_item),
        "server": server,
        "server_port": int(proxy_item.get("port") or 443),
        "password": password,
        "tls": compact_dict(
            {
                "server_name": str(proxy_item.get("sni") or proxy_item.get("servername") or "").strip(),
                "alpn": proxy_item.get("alpn") if isinstance(proxy_item.get("alpn"), list) else None,
                "insecure": bool(proxy_item.get("skip-cert-verify") or proxy_item.get("skip_cert_verify")),
            }
        ),
    }


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


def compatible_hysteria2(outbound: JSON) -> bool:
    return (
        outbound.get("protocol") == "hysteria2"
        and bool(str(outbound.get("server") or "").strip())
        and bool(str(outbound.get("password") or "").strip())
    )


def compatible_outbound(outbound: JSON) -> bool:
    return compatible_vless_reality(outbound) or compatible_hysteria2(outbound)


def selected_source(
    profiles: list[JSON],
    country: str,
    tag_prefix: str,
    max_outbounds: int,
    prefer_profile_aliases: list[str] | None = None,
) -> tuple[JSON, list[str]]:
    selected: list[JSON] = []
    matched_profiles: list[str] = []
    candidates: list[tuple[JSON, str]] = []
    preferred: list[tuple[JSON, str]] = []
    aliases = [alias.strip().lower() for alias in (prefer_profile_aliases or []) if alias.strip()]
    for profile in profiles:
        name = profile_name(profile)
        if not country_matches(name, country):
            continue
        item = (profile, name)
        candidates.append(item)
        if aliases and any(alias in name.lower() for alias in aliases):
            preferred.append(item)
    if preferred:
        candidates = preferred
    for profile, name in candidates:
        profile_had_compatible = False
        for outbound in profile.get("outbounds", []):
            if not isinstance(outbound, dict) or not compatible_outbound(outbound):
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
        raise SystemExit(f"no compatible VLESS TCP+Reality or Hysteria2 outbounds found for country={country}")
    return {"outbounds": selected}, matched_profiles


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def singbox_vless_from_source(outbound: JSON) -> JSON:
    vnext = nested(outbound, "settings.vnext.0")
    user = nested(outbound, "settings.vnext.0.users.0")
    reality = nested(outbound, "streamSettings.realitySettings")
    if not isinstance(vnext, dict) or not isinstance(user, dict) or not isinstance(reality, dict):
        raise SystemExit(f"invalid VLESS outbound: {outbound.get('tag')}")
    return compact_dict(
        {
            "type": "vless",
            "tag": str(outbound.get("tag") or "vless-out"),
            "server": vnext.get("address"),
            "server_port": vnext.get("port"),
            "uuid": user.get("id"),
            "flow": user.get("flow"),
            "packet_encoding": "xudp",
            "tls": compact_dict(
                {
                    "enabled": True,
                    "server_name": reality.get("serverName"),
                    "utls": {
                        "enabled": True,
                        "fingerprint": reality.get("fingerprint") or "firefox",
                    },
                    "reality": compact_dict(
                        {
                            "enabled": True,
                            "public_key": reality.get("publicKey"),
                            "short_id": reality.get("shortId"),
                        }
                    ),
                }
            ),
        }
    )


def singbox_hysteria2_from_source(outbound: JSON) -> JSON:
    tls = outbound.get("tls") if isinstance(outbound.get("tls"), dict) else {}
    return compact_dict(
        {
            "type": "hysteria2",
            "tag": str(outbound.get("tag") or "hysteria2-out"),
            "server": outbound.get("server"),
            "server_port": outbound.get("server_port"),
            "password": outbound.get("password"),
            "tls": compact_dict(
                {
                    "enabled": True,
                    "server_name": tls.get("server_name"),
                    "alpn": tls.get("alpn"),
                    "insecure": tls.get("insecure"),
                }
            ),
        }
    )


def source_outbound_to_singbox(outbound: JSON) -> JSON:
    if compatible_vless_reality(outbound):
        return singbox_vless_from_source(outbound)
    if compatible_hysteria2(outbound):
        return singbox_hysteria2_from_source(outbound)
    raise SystemExit(f"unsupported selected outbound: {outbound.get('tag')}")


def write_direct_singbox_config(source: JSON, args: argparse.Namespace, target_name: str, output_path: Path) -> None:
    converted_outbounds = [source_outbound_to_singbox(outbound) for outbound in source.get("outbounds", [])]
    if not converted_outbounds:
        raise SystemExit(f"{target_name}: no compatible outbounds selected")

    outbound_tags = [str(outbound["tag"]) for outbound in converted_outbounds]
    generated_outbounds: list[JSON] = []
    if len(outbound_tags) > 1:
        final_tag = f"{target_name}-auto"
        generated_outbounds.append(
            {
                "type": "urltest",
                "tag": final_tag,
                "outbounds": outbound_tags,
                "url": "http://www.gstatic.com/generate_204",
                "interval": "5m",
                "tolerance": 50,
            }
        )
    else:
        final_tag = outbound_tags[0]

    generated_outbounds.extend(converted_outbounds)
    generated_outbounds.extend([{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}])
    result: JSON = {
        "log": {"level": "warn", "timestamp": True},
        "inbounds": [
            {
                "type": "socks",
                "tag": f"{target_name}-socks-in",
                "listen": args.listen,
                "listen_port": int(TARGETS[target_name]["listen_port"]),
                "sniff": True,
            }
        ],
        "outbounds": generated_outbounds,
        "route": {"auto_detect_interface": True, "final": final_tag},
    }
    output_path.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    run(["/usr/bin/sing-box", "check", "-c", str(output_path)])


def convert_target(source: JSON, args: argparse.Namespace, target_name: str, target: JSON, workdir: Path) -> Path:
    source_path = workdir / f"{target_name}-source.json"
    output_path = workdir / f"{target_name}-singbox.json"
    source_path.write_text(json.dumps(source, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    protocols = {str(outbound.get("protocol") or "") for outbound in source.get("outbounds", []) if isinstance(outbound, dict)}
    if protocols == {"vless"}:
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
    else:
        write_direct_singbox_config(source, args, target_name, output_path)
        log(f"{target_name}: direct_singbox=ok protocols={','.join(sorted(protocols))}")
    probe_url = str(target.get("probe_url") or "").strip()
    if probe_url:
        converted = json.loads(output_path.read_text(encoding="utf-8"))
        for outbound in converted.get("outbounds", []):
            if isinstance(outbound, dict) and outbound.get("type") == "urltest":
                outbound["url"] = probe_url
                if env_bool(str(target.get("filter_outbounds_by_probe", "")), False):
                    outbound["outbounds"] = filter_working_outbounds(converted, outbound, target_name, target, workdir, args)
        output_path.write_text(json.dumps(converted, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        run(["/usr/bin/sing-box", "check", "-c", str(output_path)])
    return output_path


def filter_working_outbounds(
    converted: JSON,
    urltest: JSON,
    target_name: str,
    target: JSON,
    workdir: Path,
    args: argparse.Namespace,
) -> list[str]:
    tags = [tag for tag in urltest.get("outbounds", []) if isinstance(tag, str) and tag.strip()]
    if len(tags) <= 1:
        return tags

    working: list[str] = []
    probe_url = str(target["probe_url"])
    base_port = int(target["listen_port"]) + 1000
    for idx, tag in enumerate(tags):
        probe_config = copy.deepcopy(converted)
        probe_port = base_port + idx
        probe_config["inbounds"][0]["listen_port"] = probe_port
        for outbound in probe_config.get("outbounds", []):
            if isinstance(outbound, dict) and outbound.get("tag") == urltest.get("tag"):
                outbound["outbounds"] = [tag]
                outbound["url"] = probe_url
        probe_path = workdir / f"{target_name}-{tag}-probe.json"
        probe_path.write_text(json.dumps(probe_config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        proc = subprocess.Popen(
            ["/usr/bin/sing-box", "-D", str(workdir), "-c", str(probe_path), "run"],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            time.sleep(0.7)
            ok = probe_proxy(
                probe_port,
                probe_url,
                1,
                args.probe_timeout,
                follow_redirects=env_bool(str(target.get("probe_follow_redirects", "")), False),
            )
            if ok:
                working.append(tag)
        finally:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.communicate(timeout=2)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.communicate(timeout=2)

    if not working:
        raise SystemExit(f"{target_name}: no outbounds passed probe_url={probe_url}")
    log(f"{target_name}: working_outbounds={','.join(working)}")
    return working


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


def probe_proxy(port: int, url: str, attempts: int, timeout: int, *, follow_redirects: bool = False) -> bool:
    for attempt in range(1, attempts + 1):
        cmd = [
            "curl",
            "-sS",
            "--max-time",
            str(timeout),
            "--socks5-hostname",
            f"127.0.0.1:{port}",
            url,
            "-o",
            "/dev/null",
            "-w",
            "http=%{http_code} total=%{time_total}",
        ]
        if follow_redirects:
            cmd.insert(2, "-L")
        else:
            cmd.insert(2, "-I")
        proc = run(
            cmd,
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
            source, profile_names = selected_source(
                profiles,
                country,
                str(target["tag_prefix"]),
                args.max_outbounds,
                list(target.get("prefer_profile_aliases") or []),
            )
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
                ok = probe_proxy(
                    int(target["listen_port"]),
                    str(target["probe_url"]),
                    args.probe_attempts,
                    args.probe_timeout,
                    follow_redirects=env_bool(str(target.get("probe_follow_redirects", "")), False),
                )
                if not ok:
                    if target_name in installed:
                        destination, backup = installed[target_name]
                        restore_backup(destination, backup, str(target["service"]))
                    failures.append(target_name)
            if failures:
                raise SystemExit("probe failed, rolled back: " + ",".join(failures))

    log("done")


if __name__ == "__main__":
    main()
