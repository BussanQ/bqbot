"""Prepare signing material only on an ephemeral release runner; never print secrets."""
import base64
import os
from pathlib import Path
import secrets
import subprocess
import sys

root = Path(os.environ["RUNNER_TEMP"])
keychain = root / "bqbot-signing.keychain-db"
certificate = root / "bqbot-certificate.p12"
windows_certificate = root / "bqbot-code-signing.pfx"


def run(*args):
    result = subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if result.returncode:
        raise RuntimeError(f"Signing setup failed: {args[0]}")


def required(name):
    value = os.environ.get(name)
    if not value:
        raise RuntimeError(f"Missing signing secret: {name}")
    return value


def private_file(path, value):
    with path.open("xb") as target:
        os.chmod(path, 0o600)
        target.write(base64.b64decode(value, validate=True))


if "--clean" in sys.argv:
    for path in (certificate, windows_certificate):
        path.unlink(missing_ok=True)
    if os.environ["RELEASE_PLATFORM"] == "darwin" and keychain.exists():
        run("security", "delete-keychain", str(keychain))
    sys.exit(0)

with open(os.environ["GITHUB_ENV"], "a", encoding="utf-8") as env:
    if os.environ["RELEASE_PLATFORM"] == "darwin":
        password = secrets.token_urlsafe(32)
        private_file(certificate, required("MACOS_CERTIFICATE"))
        run("security", "create-keychain", "-p", password, str(keychain))
        run("security", "set-keychain-settings", "-lut", "21600", str(keychain))
        run("security", "unlock-keychain", "-p", password, str(keychain))
        run("security", "import", str(certificate), "-k", str(keychain), "-P", required("MACOS_CERTIFICATE_PASSWORD"), "-T", "/usr/bin/codesign")
        certificate.unlink()
        run("security", "set-key-partition-list", "-S", "apple-tool:,apple:", "-s", "-k", password, str(keychain))
        run("security", "list-keychains", "-d", "user", "-s", str(keychain), "login.keychain")
        run("xcrun", "notarytool", "store-credentials", "bqbot-notary", "--keychain", str(keychain), "--apple-id", required("APPLE_ID"), "--team-id", required("APPLE_TEAM_ID"), "--password", required("APPLE_APP_PASSWORD"))
        env.write(f"NOTARY_PROFILE=bqbot-notary\nNOTARY_KEYCHAIN={keychain}\n")
    elif os.environ["RELEASE_PLATFORM"] == "windows":
        private_file(windows_certificate, required("WINDOWS_CERTIFICATE"))
        env.write(f"WINDOWS_CERTIFICATE_FILE={windows_certificate}\n")
