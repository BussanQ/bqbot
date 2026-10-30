#!/usr/bin/env python3
"""Stage the pinned macOS Cua executable for MyGo; run with python3 -I."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
VERSION = "0.28.2"
TAG = "cua-driver-rs-v" + VERSION
ASSET = "cua-driver-rs-0.28.2-darwin-universal-binary.tar.gz"
URL = "https://github.com/trycua/cua/releases/download/" + TAG + "/" + ASSET
ARCHIVE_SHA256 = "386db225a3080714a0f9f935525e61efaf46709587ef8b94dd2df81aeb2f6daa"
BINARY_SHA256 = "af30d29cf33bd3bbda1330be7225b18881ea4c5af6df374e08627914b5ac334d"
ARCHIVE_SIZE = 43629843
MEMBERS = {
    "cua-driver": 65948880,
    "cua-cursor-theme": 5347168,
    "libcua_driver_sdk.dylib": 51741008,
    "cua_driver_node_runtime.node": 1712400,
    "cua_driver_abi.h": 7762,
}
STAGED = {"cua-driver", "manifest.json", "LICENSE", "NOTICE.md"}


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def manifest():
    return {
        "version": VERSION,
        "platform": "darwin-universal",
        "minimum_macos": "13.0",
        "source": URL,
        "archive_sha256": ARCHIVE_SHA256,
        "binary_sha256": BINARY_SHA256,
    }


def needs_driver(targets):
    platforms = set()
    for target in targets.split(","):
        parts = target.strip().split("/")
        if parts[0] not in {"darwin", "linux", "windows"} or len(parts) > 2:
            raise ValueError("unsupported build target: " + target)
        if len(parts) == 2 and parts[1] not in {"arm64", "amd64", "universal"}:
            raise ValueError("unsupported architecture: " + target)
        platforms.add(parts[0])
    return "darwin" in platforms


def extract_driver(archive, destination):
    """Validate every entry before copying only the standalone CLI, never extractall."""
    with tarfile.open(archive, "r:gz") as source:
        found = {}
        for entry in source:
            if (entry.name not in MEMBERS or entry.name in found
                    or not entry.isfile() or entry.issparse()
                    or entry.size != MEMBERS[entry.name] or entry.pax_headers):
                raise ValueError("unexpected Cua archive entry: " + entry.name)
            found[entry.name] = entry
        if set(found) != set(MEMBERS):
            raise ValueError("Cua archive is missing expected entries")
        with source.extractfile(found["cua-driver"]) as src, destination.open("xb") as dst:
            shutil.copyfileobj(src, dst, 1024 * 1024)
    if sha256(destination) != BINARY_SHA256:
        raise ValueError("Cua binary SHA-256 mismatch")
    destination.chmod(0o755)


def checked_directory(path):
    # Generated resource paths must not redirect writes outside the checkout.
    for parent in [path, *path.parents]:
        if parent.is_symlink():
            raise ValueError("refusing symlink resource path: " + str(parent))
    path.mkdir(parents=True, exist_ok=True)
    for child in path.iterdir():
        if child.name not in STAGED or child.is_symlink() or not child.is_file():
            raise ValueError("unexpected existing resource: " + str(child))


def current(destination, notices):
    try:
        return (sha256(destination / "cua-driver") == BINARY_SHA256
                and os.access(destination / "cua-driver", os.X_OK)
                and json.loads((destination / "manifest.json").read_text()) == manifest()
                and all((destination / name).read_bytes() == (notices / name).read_bytes()
                        for name in ("LICENSE", "NOTICE.md")))
    except (OSError, ValueError):
        return False


def prepare(archive=None):
    destination = ROOT / "resources/darwin/cua-driver"
    notices = ROOT / "third_party/cua-driver"
    checked_directory(destination)
    if archive is None and current(destination, notices):
        print("Cua Driver " + VERSION + ": verified staged executable")
        return
    # Downloads and extraction each get a new empty directory. No downloaded
    # program is executed; the Python interpreter stays outside both directories.
    with tempfile.TemporaryDirectory(prefix="bqbot-cua-download-") as download:
        if archive is None:
            archive = Path(download) / ASSET
            subprocess.run([
                "curl", "--fail", "--silent", "--show-error", "--location",
                "--proto", "=https", "--proto-redir", "=https", "--tlsv1.2",
                "--connect-timeout", "30", "--max-time", "600",
                "--max-filesize", str(ARCHIVE_SIZE), "--output", str(archive), URL,
            ], check=True)
        if archive.is_symlink() or not archive.is_file():
            raise ValueError("Cua archive must be a regular file")
        if archive.stat().st_size != ARCHIVE_SIZE or sha256(archive) != ARCHIVE_SHA256:
            raise ValueError("Cua archive SHA-256 or size mismatch")
        with tempfile.TemporaryDirectory(prefix="bqbot-cua-extract-") as unpack:
            staging = Path(unpack)
            extract_driver(archive, staging / "cua-driver")
            (staging / "manifest.json").write_text(json.dumps(manifest(), indent=2) + "\n")
            for name in ("LICENSE", "NOTICE.md"):
                shutil.copyfile(notices / name, staging / name)
            for name in sorted(STAGED):
                # Same-filesystem temporary file makes each replacement atomic.
                with tempfile.NamedTemporaryFile(dir=destination, prefix=".stage-", delete=False) as output:
                    pending = Path(output.name)
                try:
                    shutil.copyfile(staging / name, pending)
                    pending.chmod(0o755 if name == "cua-driver" else 0o644)
                    os.replace(pending, destination / name)
                finally:
                    pending.unlink(missing_ok=True)
    print("Cua Driver " + VERSION + ": staged " + str(destination))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    default_target = os.environ.get("RELEASE_TARGETS") or {"darwin": "darwin", "win32": "windows"}.get(sys.platform, sys.platform)
    parser.add_argument("--targets", default=default_target, help="MyGo targets; defaults to RELEASE_TARGETS or host OS")
    parser.add_argument("--archive", type=Path, help="offline copy of the exact pinned official archive")
    args = parser.parse_args()
    if not needs_driver(args.targets):
        print("Embedded Cua host control is currently macOS-only; no driver staged for " + args.targets)
        return
    prepare(args.archive)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, tarfile.TarError, subprocess.CalledProcessError) as error:
        sys.exit("prepare-cua: " + str(error))
