# Cua Driver attribution and provenance

bqbot bundles the standalone Cua Driver CLI, copyright (c) 2025 Cua AI, Inc., under the accompanying MIT LICENSE.

- Upstream: https://github.com/trycua/cua
- Release: https://github.com/trycua/cua/releases/tag/cua-driver-rs-v0.28.2
- Source commit: `fc188250b4ca8549b8e61f937fdb1fb560770e86`
- License source: https://github.com/trycua/cua/blob/fc188250b4ca8549b8e61f937fdb1fb560770e86/LICENSE.md
- Asset: `cua-driver-rs-0.28.2-darwin-universal-binary.tar.gz`
- Archive SHA-256: `386db225a3080714a0f9f935525e61efaf46709587ef8b94dd2df81aeb2f6daa`
- Original CLI SHA-256: `af30d29cf33bd3bbda1330be7225b18881ea4c5af6df374e08627914b5ac334d`

The archive SHA-256 matches the official GitHub release asset digest and a local download. Both Mach-O slices (arm64 and x86_64) declare macOS 13.0, matching the pinned release workflow's deployment target. Compatibility on older machines still requires real-device acceptance.

Only `cua-driver` is redistributed. The archive's cursor-theme tool, UniFFI dylib, Node runtime and C header are not needed by the CLI/stdio embedding and are not shipped. The pinned bare binary archive contains no license files; the repository root license is therefore included explicitly. No upstream installer or downloaded script is executed. This is an upstream compiled CLI; this file is attribution and provenance, not an independently generated dependency SBOM.

The packaged executable is re-signed with the bqbot release identity. `manifest.json` records upstream hashes for provenance; re-signing changes the binary hash, so a production app validates its bundle/code signatures. Development resources retain the original upstream bytes. Cua is a third-party component, and upstream does not endorse bqbot.
