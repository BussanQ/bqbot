"""Run: python3 -I tests/prepare_cua_test.py"""
import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("prepare_cua", ROOT / "scripts/prepare-cua.py")
cua = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cua)


class PrepareCuaTest(unittest.TestCase):
    def archive(self, path, entries):
        with tarfile.open(path, "w:gz", format=tarfile.USTAR_FORMAT) as archive:
            for name, kind in entries:
                entry = tarfile.TarInfo(name)
                entry.type = kind
                entry.size = 4 if kind == tarfile.REGTYPE else 0
                entry.linkname = "outside" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ""
                archive.addfile(entry, io.BytesIO(b"test") if entry.size else None)

    def test_only_allowlisted_regular_entries(self):
        cases = [
            [("../cua-driver", tarfile.REGTYPE)],
            [("/cua-driver", tarfile.REGTYPE)],
            [("cua-driver", tarfile.SYMTYPE)],
            [("cua-driver", tarfile.LNKTYPE)],
            [("cua-driver", tarfile.DIRTYPE)],
            [("cua-driver", tarfile.REGTYPE), ("cua-driver", tarfile.REGTYPE)],
            [("cua-driver", tarfile.REGTYPE), ("json.py", tarfile.REGTYPE)],
            [],
        ]
        for entries in cases:
            with self.subTest(entries=entries), tempfile.TemporaryDirectory() as temp:
                archive, output = Path(temp) / "a.tar.gz", Path(temp) / "cua-driver"
                self.archive(archive, entries)
                with patch.object(cua, "MEMBERS", {"cua-driver": 4}):
                    with self.assertRaises(ValueError):
                        cua.extract_driver(archive, output)
                self.assertFalse(output.exists())

    def test_verified_binary_and_size(self):
        with tempfile.TemporaryDirectory() as temp:
            archive, output = Path(temp) / "a.tar.gz", Path(temp) / "cua-driver"
            self.archive(archive, [("cua-driver", tarfile.REGTYPE)])
            with patch.object(cua, "MEMBERS", {"cua-driver": 4}), patch.object(cua, "BINARY_SHA256", hashlib.sha256(b"test").hexdigest()):
                cua.extract_driver(archive, output)
                self.assertEqual(output.read_bytes(), b"test")
                self.assertEqual(output.stat().st_mode & 0o777, 0o755)
            output.unlink()
            with patch.object(cua, "MEMBERS", {"cua-driver": 5}), self.assertRaises(ValueError):
                cua.extract_driver(archive, output)
            with patch.object(cua, "MEMBERS", {"cua-driver": 4}), self.assertRaisesRegex(ValueError, "SHA-256"):
                cua.extract_driver(archive, output)

    def test_bad_archive_leaves_existing_driver(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            target = root / "resources/darwin/cua-driver"
            target.mkdir(parents=True)
            (target / "cua-driver").write_bytes(b"existing")
            archive = root / "tampered.tar.gz"
            archive.write_bytes(b"tampered")
            with patch.object(cua, "ROOT", root), self.assertRaisesRegex(ValueError, "SHA-256"):
                cua.prepare(archive)
            self.assertEqual((target / "cua-driver").read_bytes(), b"existing")

    def test_symlinks_and_unexpected_existing_files_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            target = root / "resource"
            target.symlink_to(root, target_is_directory=True)
            with self.assertRaises(ValueError):
                cua.checked_directory(target)
            target.unlink()
            target.mkdir()
            (target / "unrelated").write_text("keep")
            with self.assertRaises(ValueError):
                cua.checked_directory(target)

    def test_cached_resources_are_reverified(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            destination, notices = root / "stage", root / "notices"
            destination.mkdir()
            notices.mkdir()
            for name in ("LICENSE", "NOTICE.md"):
                (notices / name).write_text(name)
                (destination / name).write_text(name)
            binary = destination / "cua-driver"
            binary.write_bytes(b"test")
            binary.chmod(0o755)
            with patch.object(cua, "BINARY_SHA256", hashlib.sha256(b"test").hexdigest()):
                (destination / "manifest.json").write_text(cua.json.dumps(cua.manifest()))
                self.assertTrue(cua.current(destination, notices))
                binary.write_bytes(b"tampered")
                self.assertFalse(cua.current(destination, notices))
                binary.write_bytes(b"test")
                (destination / "manifest.json").write_text('{}')
                self.assertFalse(cua.current(destination, notices))

    def test_platform_selection(self):
        self.assertTrue(cua.needs_driver("darwin/amd64,darwin/arm64"))
        self.assertTrue(cua.needs_driver("linux/amd64,darwin/universal"))
        self.assertFalse(cua.needs_driver("windows/arm64,linux/amd64"))
        for target in ("freebsd/amd64", "darwin/386", "darwin/arm64/extra", ""):
            with self.assertRaises(ValueError):
                cua.needs_driver(target)


if __name__ == "__main__":
    unittest.main()
