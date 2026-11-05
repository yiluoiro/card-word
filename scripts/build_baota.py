"""Build a clean Linux release from an explicit file allowlist; never copy dist/data."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True, encoding="utf-8").strip()


def pack(package, archive):
    files = sorted(p for p in package.rglob("*") if p.is_file())
    checksum = "".join(hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.relative_to(package).as_posix() + "\n" for p in files)
    (package / "SHA256SUMS").write_text(checksum, encoding="utf-8", newline="\n")
    def normalize(member):
        member.uid = member.gid = 0
        member.uname = member.gname = "root"
        member.mode = 0o755 if member.isdir() or member.name.endswith(".sh") or member.name.endswith("/cardword") else 0o644
        return member
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(package, arcname=package.name, filter=normalize)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_name(archive.name + ".sha256").write_text(digest + "  " + archive.name + "\n", encoding="ascii")
    print(archive)
    print("SHA256:", digest)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", default="v1.0.0")
    parser.add_argument("--output", type=Path, default=ROOT / "dist" / "releases")
    parser.add_argument("--panel", action="store_true", help="build separate single-binary packages for BaoTa Go project management")
    args = parser.parse_args()
    if not all(c.isalnum() or c in ".-_" for c in args.version):
        raise SystemExit("Invalid version")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = "cardword-baota-" + args.version
    archive = output / (name + ".tar.gz")
    archives = [output / f"cardword-panel-{args.version}-linux-{arch}.tar.gz" for arch in ["amd64", "arm64"]] if args.panel else [archive]
    for target in archives:
        if target.exists():
            raise SystemExit(f"Refusing to overwrite {target}; choose a new version or output directory")
    built = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    commit = "source-archive"
    if (ROOT / ".git").exists():
        commit = run("git", "rev-parse", "--short", "HEAD")
        if run("git", "status", "--porcelain"):
            commit += "-working-tree"
    with tempfile.TemporaryDirectory(prefix="cardword-release-") as temporary:
        package = Path(temporary) / name
        package.mkdir()
        filenames = ["start.sh", "README.md"] if args.panel else ["install.sh", "backup.sh", "cardword.service", "nginx-location.conf", "README.md"]
        for filename in filenames:
            source = ROOT / "deploy" / ("baota-panel" if args.panel else "baota") / filename
            (package / filename).write_text(source.read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
        shutil.copy2(ROOT / "LICENSE", package / "LICENSE")
        shutil.copy2(ROOT / "NOTICE", package / "NOTICE")
        shutil.copy2(ROOT / "docs" / "COMMERCE_CONFIGURATION.md", package / "COMMERCE_CONFIGURATION.md")
        for arch in ["amd64", "arm64"]:
            target = package / "bin" / ("linux-" + arch) / "cardword"
            target.parent.mkdir(parents=True)
            environment = os.environ.copy()
            environment.update(GOOS="linux", GOARCH=arch, CGO_ENABLED="0", GOAMD64="v1", GOARM64="v8.0")
            flags = "-s -w -X github.com/yiluoiro/card-word/internal/version.Version=" + args.version
            flags += " -X github.com/yiluoiro/card-word/internal/version.Commit=" + commit
            flags += " -X github.com/yiluoiro/card-word/internal/version.Date=" + built
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", str(target), "./cmd/cardword"], cwd=ROOT, env=environment, check=True)
            raw = target.read_bytes()
            assert raw[:4] == b"\x7fELF" and raw[4] == 2 and raw[5] == 1
            assert int.from_bytes(raw[18:20], "little") == (62 if arch == "amd64" else 183)
            # No interpreter segment: releases must not depend on host glibc.
            offset = int.from_bytes(raw[32:40], "little")
            size = int.from_bytes(raw[54:56], "little")
            count = int.from_bytes(raw[56:58], "little")
            assert all(int.from_bytes(raw[offset+i*size:offset+i*size+4], "little") != 3 for i in range(count))
            print(f"Built linux/{arch}: {len(raw)} bytes", flush=True)
        (package / "DEPENDENCIES.txt").write_text(run("go", "list", "-m", "all") + "\n", encoding="utf-8")
        # Preserve available upstream license texts with the shipped binaries.
        modules = run("go", "list", "-m", "-json", "all")
        decoder = json.JSONDecoder()
        while modules.strip():
            module, end = decoder.raw_decode(modules.lstrip())
            modules = modules.lstrip()[end:]
            if module.get("Main") or not module.get("Dir"):
                continue
            for candidate in Path(module["Dir"]).iterdir():
                if candidate.is_file() and candidate.name.upper().startswith(("LICENSE", "LICENCE", "COPYING", "NOTICE")):
                    target = package / "licenses" / module["Path"].replace("/", "_") / candidate.name
                    target.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(candidate, target)
        goroot = Path(run("go", "env", "GOROOT"))
        (package / "licenses" / "go").mkdir(parents=True, exist_ok=True)
        shutil.copyfile(goroot / "LICENSE", package / "licenses" / "go" / "LICENSE")
        (package / "licenses" / "simple-icons.txt").write_text("Alipay, WeChat and QQ SVG marks from Simple Icons, licensed CC0-1.0.\nhttps://github.com/simple-icons/simple-icons\nBrand trademarks remain the property of their respective owners.\n", encoding="utf-8")
        info = f"Version: {args.version}\nSource: {commit}\nBuilt (UTC): {built}\nCompiler: {run('go','version')}\nTargets: linux/amd64 (v1), linux/arm64 (v8.0)\nCGO: disabled; ELF static binaries verified\nNo databases, credentials or encryption keys included.\nLinux systemd/BaoTa runtime verification must be performed on the target server.\n"
        (package / "BUILD-INFO.txt").write_text(info, encoding="utf-8")
        if args.panel:
            for arch, target in zip(["amd64", "arm64"], archives):
                single = Path(temporary) / arch / "cardword"
                shutil.copytree(package, single, ignore=shutil.ignore_patterns("bin"))
                shutil.copy2(package / "bin" / ("linux-" + arch) / "cardword", single / "cardword")
                (single / "BUILD-INFO.txt").write_text(info + f"This package: linux/{arch}; BaoTa Go project mode.\n", encoding="utf-8")
                pack(single, target)
        else:
            pack(package, archive)


if __name__ == "__main__":
    main()
