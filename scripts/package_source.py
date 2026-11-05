"""Export only community source files, without runtime data or parent files."""
from pathlib import Path
import hashlib
import zipfile

ROOT = Path(__file__).resolve().parents[1]
ROOT_FILES = ['README.md', 'NOTICE', 'LICENSE', 'go.mod', 'go.sum', 'Dockerfile',
              'docker-compose.yml', 'Makefile', '.gitignore', '.dockerignore', '.gitattributes']
SOURCE_DIRS = ['cmd', 'internal', 'examples', 'deploy', 'docs', 'scripts', '.github']
SUFFIXES = {'.go', '.sql', '.html', '.css', '.js', '.json', '.svg', '.png', '.webp',
            '.jpg', '.jpeg', '.ico', '.md', '.py', '.sh', '.service', '.conf', '.yml', '.yaml'}
EXCLUDED = {'.git', '__pycache__', 'data', 'tmpdata', 'tmpkeys', 'keys', 'dist', 'node_modules'}

def main():
    files = [ROOT / name for name in ROOT_FILES]
    for directory in SOURCE_DIRS:
        for path in (ROOT / directory).rglob('*'):
            if path.is_symlink():
                raise SystemExit(f'Refusing symbolic link: {path}')
            relative = path.relative_to(ROOT)
            if path.is_file() and not EXCLUDED.intersection(relative.parts) and path.suffix.lower() in SUFFIXES:
                files.append(path)
    output = ROOT / 'dist' / 'cardword-community-source.zip'
    output.parent.mkdir(exist_ok=True)
    with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(set(files)):
            archive.write(path, 'cardword-community/' + path.relative_to(ROOT).as_posix())
    digest = hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_suffix('.zip.sha256').write_text(digest + '  ' + output.name + '\n', encoding='ascii')
    print(output)
    print(f'{len(set(files))} files; SHA256 {digest}')

if __name__ == '__main__':
    main()
