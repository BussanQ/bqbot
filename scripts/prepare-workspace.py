"""Stage a self-contained Docker build context beside the desktop executable."""
import os
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parents[1]
context = root / "resources/workspace-image"
for name in ("workspace/Dockerfile", "workspace/desktop.py", "workspace/start.sh",
             "workspace/install-desktop.sh", "workspace/panel.xml", "workspace/workspace-browser.desktop",
             "internal/computer/workspace-action.json"):
    target = context / name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(root / name, target)

for arch in ("arm64", "amd64"):
    target = context / "workspace/bin" / ("bridge-" + arch)
    target.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["go", "build", "-trimpath", "-o", str(target), "./bridge"],
                   cwd=root / "workspace", check=True,
                   env={**os.environ, "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"})
