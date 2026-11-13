"""Prepare resources for the MyGo packaging hook."""
from pathlib import Path
import subprocess
import sys

for script in ("prepare-cua.py", "prepare-workspace.py"):
    subprocess.run([sys.executable, "-I", str(Path(__file__).with_name(script))], check=True)
