#!/bin/sh
# Build Collie for macOS and Linux and atomically publish a GitHub Release
# via Draft -> upload 5/5 assets -> verify -> PATCH draft=false.
#
#   scripts/release.sh v0.1.0          build, then publish the release
#   scripts/release.sh v0.1.0 --dry    build only (into dist/)
#
# Auth: uses GH_TOKEN or GITHUB_TOKEN; otherwise falls back to official
# GitHub CLI (`gh auth token`) or ~/.git-credentials for github.com.
#
# install.sh downloads releases/latest/download/collie-<os>-<arch> and
# checksums.txt, so the asset names below are part of the contract.
set -eu

version=${1:?usage: scripts/release.sh vX.Y.Z [--dry]}
dry=${2:-}
cd "$(dirname "${0}")/.."

case "${version}" in v[0-9]*) ;; *) echo "version must look like v0.1.0" >&2; exit 2 ;; esac
if [ -n "${dry}" ] && [ "${dry}" != "--dry" ]; then
  echo "usage: scripts/release.sh vX.Y.Z [--dry]" >&2
  exit 2
fi
if [ -z "${dry}" ] && [ -n "$(git status --porcelain)" ]; then
  echo "commit your changes first: a release is built from what is committed" >&2
  exit 1
fi

rm -rf dist && mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*}
  arch=${target#*/}
  out="dist/collie-${os}-${arch}"
  echo "building ${out}"
  GOOS="${os}" GOARCH="${arch}" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "${out}" .
done
if command -v shasum >/dev/null 2>&1; then
  (cd dist && shasum -a 256 collie-* > checksums.txt)
else
  (cd dist && sha256sum collie-* > checksums.txt)
fi
cat dist/checksums.txt

[ "${dry}" = "--dry" ] && exit 0

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to publish a GitHub Release" >&2
  exit 1
fi

repo=${GITHUB_REPO:-}
if [ -z "${repo}" ]; then
  origin_url=$(git remote get-url origin 2>/dev/null || true)
  case "${origin_url}" in
    *github.com/*) repo=${origin_url#*github.com/} ;;
    *github.com:*) repo=${origin_url#*github.com:} ;;
    *) repo="lihuanyu0w0/collie" ;;
  esac
  repo=${repo%/}
  repo=${repo%.git}
fi

if [ -z "${GH_TOKEN:-}" ] && [ -n "${GITHUB_TOKEN:-}" ]; then
  GH_TOKEN=${GITHUB_TOKEN}
fi
if [ -z "${GH_TOKEN:-}" ] && command -v gh >/dev/null 2>&1; then
  case "$(gh --version 2>/dev/null || true)" in
    *"github.com/cli/cli"*)
      GH_TOKEN=$(gh auth token 2>/dev/null || true)
      ;;
  esac
fi

git tag -a "${version}" -m "Collie ${version}" 2>/dev/null || true
git push origin "${version}"

RELEASE_TAG="${version}" RELEASE_REPO="${repo}" GH_TOKEN="${GH_TOKEN:-}" python3 - <<'PY'
import json
import os
import pathlib
import urllib.error
import urllib.parse
import urllib.request

tag = os.environ["RELEASE_TAG"]
repo = os.environ["RELEASE_REPO"]
owner = repo.split("/", 1)[0]
notes = (
    "安装：`curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh`"
    "（做 hub 加 `-s -- hub`，加入电脑见 README）。也可以直接下载下面对应平台的二进制。"
)

token = os.environ.get("GH_TOKEN", "").strip()
if not token:
    cred_file = pathlib.Path.home() / ".git-credentials"
    if cred_file.is_file():
        for raw in cred_file.read_text(encoding="utf-8").splitlines():
            raw = raw.strip()
            if not raw or "://" not in raw:
                continue
            u = urllib.parse.urlsplit(raw)
            if u.hostname == "github.com" and u.password:
                token = urllib.parse.unquote(u.password)
                if urllib.parse.unquote(u.username or "") == owner:
                    break
if not token:
    raise SystemExit(
        "ERROR: GitHub token not found; set GH_TOKEN / GITHUB_TOKEN "
        "or authenticate with GitHub CLI"
    )


def api(method, url, data=None, ctype="application/json"):
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
        "User-Agent": "collie-release-script",
    }
    if data is not None:
        headers["Content-Type"] = ctype
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            raw = resp.read()
            return json.loads(raw.decode("utf-8")) if raw else None
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
        raise SystemExit(f"ERROR: {method} {url} -> HTTP {e.code}: {body}") from None


# 1) Reuse existing draft/published release for this tag, or create a draft release.
releases = api("GET", f"https://api.github.com/repos/{repo}/releases?per_page=30")
matches = [r for r in releases if r.get("tag_name") == tag]
rel = next((r for r in matches if not r.get("draft")), matches[0] if matches else None)
if rel is None:
    rel = api(
        "POST",
        f"https://api.github.com/repos/{repo}/releases",
        json.dumps(
            {
                "tag_name": tag,
                "name": f"Collie {tag}",
                "body": notes,
                "draft": True,
                "prerelease": False,
            }
        ).encode(),
    )
    print(f"created draft: {rel['tag_name']} (id={rel['id']}, draft={rel['draft']})")
else:
    print(f"reusing release: {rel['tag_name']} (id={rel['id']}, draft={rel['draft']})")

# 2) Upload all 5 assets; delete any existing asset with the same name first.
rel_id = rel["id"]
existing = {a["name"]: a["id"] for a in rel.get("assets", [])}
upload_base = rel["upload_url"].split("{", 1)[0]
assets = [
    "collie-darwin-arm64",
    "collie-darwin-amd64",
    "collie-linux-amd64",
    "collie-linux-arm64",
    "checksums.txt",
]

for name in assets:
    if name in existing:
        api("DELETE", f"https://api.github.com/repos/{repo}/releases/assets/{existing[name]}")
    payload = (pathlib.Path("dist") / name).read_bytes()
    query = urllib.parse.urlencode({"name": name})
    res = api("POST", f"{upload_base}?{query}", payload, ctype="application/octet-stream")
    print(f"uploaded: {res['name']} ({res['size']} bytes, state={res['state']})")

# 3) Re-fetch release and verify all 5/5 assets are uploaded with size > 0.
checked = api("GET", f"https://api.github.com/repos/{repo}/releases/{rel_id}")
checked_assets = checked.get("assets", [])
by_name = {a["name"]: a for a in checked_assets}
bad = [
    name
    for name in assets
    if name not in by_name
    or by_name[name].get("state") != "uploaded"
    or (by_name[name].get("size") or 0) <= 0
]
if bad or len(checked_assets) != len(assets):
    raise SystemExit(
        f"ERROR: release {tag} (id={rel_id}) asset check failed: "
        f"bad={bad}, count={len(checked_assets)}/{len(assets)}; keeping draft unpublished"
    )
print(f"verified {len(checked_assets)}/{len(assets)} assets uploaded (size > 0)")

# 4) Atomically publish the draft once all assets are verified.
if checked.get("draft"):
    pub = api(
        "PATCH",
        f"https://api.github.com/repos/{repo}/releases/{rel_id}",
        json.dumps({"draft": False}).encode(),
    )
    if pub.get("draft"):
        raise SystemExit(f"ERROR: release {tag} (id={rel_id}) remained draft after PATCH")
    print(f"published: {pub['tag_name']} (id={pub['id']}, draft={pub['draft']}, url={pub['html_url']})")
else:
    print(f"already published: {checked['tag_name']} (id={checked['id']}, url={checked['html_url']})")
PY
