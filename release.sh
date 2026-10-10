#!/bin/bash
# Release script for Mutiauk
# Bumps version, tags, cross-compiles, creates GitHub release with assets
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# --- Configuration ---
API_URL="https://api.github.com"
UPLOAD_URL="https://uploads.github.com"
REPO="postalsys/Mutiauk"
VERSION_FILE="VERSION"
BINARY="mutiauk"
TOKEN="${GITHUB_TOKEN:?GITHUB_TOKEN env var is required}"

# --- Parse arguments ---
BUMP="${1:-patch}"  # patch (default), minor, or major
case "$BUMP" in
    patch|minor|major) ;;
    *) echo "Usage: $0 [patch|minor|major]"; exit 1 ;;
esac

# --- Read and bump version ---
CURRENT_VERSION=$(cat "$VERSION_FILE" | tr -d '[:space:]')
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

case "$BUMP" in
    major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
    minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
    patch) PATCH=$((PATCH + 1)) ;;
esac

NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
TAG="v${NEW_VERSION}"

echo "=== Mutiauk Release ==="
echo "Current version: $CURRENT_VERSION"
echo "Bump type:       $BUMP"
echo "New version:     $NEW_VERSION"
echo "Tag:             $TAG"
echo ""

# --- Pre-flight checks ---
echo "=== Pre-flight checks ==="

# Required tools
for cmd in docker jq curl; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Error: $cmd is required but not found"
        exit 1
    fi
done
echo "  Required tools: OK"

# Uncommitted changes (excluding VERSION)
if ! git diff --quiet --exit-code -- ':!VERSION' || ! git diff --cached --quiet --exit-code -- ':!VERSION'; then
    echo "Error: You have uncommitted changes. Commit or stash them first."
    git status --short
    exit 1
fi
echo "  Working tree: clean"

# Tag existence
if git tag -l "$TAG" | grep -q .; then
    echo "Error: Tag $TAG already exists"
    exit 1
fi
echo "  Tag $TAG: available"

# SHA256 command (macOS vs Linux)
if command -v sha256sum &>/dev/null; then
    SHA256CMD="sha256sum"
else
    SHA256CMD="shasum -a 256"
fi

echo ""

# --- Build binaries inside Docker ---
# gVisor requires Linux and a matching Go toolchain, so we build in Docker
echo "=== Building binaries (Docker) ==="

GO_VERSION="1.25"
LDFLAGS="-s -w -buildid= -X github.com/postalsys/mutiauk/internal/cli.Version=${NEW_VERSION}"

rm -rf dist/
mkdir -p dist/

# Build both architectures inside a single Docker run
docker run --rm \
    -v "${SCRIPT_DIR}:/src" \
    -w /src \
    -e CGO_ENABLED=0 \
    "golang:${GO_VERSION}" \
    sh -c "
        apt-get update -qq && apt-get install -y -qq upx >/dev/null 2>&1 || true
        echo '  Building linux/amd64...'
        GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-linux-amd64 ./cmd/mutiauk
        echo '  Building linux/arm64...'
        GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-linux-arm64 ./cmd/mutiauk
        echo '  Building darwin/amd64...'
        GOOS=darwin GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-darwin-amd64 ./cmd/mutiauk
        echo '  Building darwin/arm64...'
        GOOS=darwin GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-darwin-arm64 ./cmd/mutiauk
        echo '  Building windows/amd64...'
        GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-windows-amd64.exe ./cmd/mutiauk
        echo '  Building windows/arm64...'
        GOOS=windows GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags='${LDFLAGS}' -o dist/${BINARY}-windows-arm64.exe ./cmd/mutiauk
        # Verify no developer-environment paths leaked into the binaries. Runs
        # before UPX (compressed binaries cannot be scanned) and covers every
        # target including the macOS ones. -trimpath should prevent leaks; this
        # is defense-in-depth that aborts the release if one slips through.
        echo '  Verifying no developer-environment leaks...'
        LEAKS_FOUND=0
        for f in dist/${BINARY}-*; do
            LEAKS=\$(strings \"\$f\" | grep -oE '/Users/[A-Za-z][A-Za-z0-9_-]+/[A-Za-z]|/home/[a-z][a-z0-9_-]+/[A-Z]|/private/var/folders/[a-z0-9_]+/|GOROOT=[^ ]+' | grep -vE '^/home/user/' | sort -u || true)
            if [ -n \"\$LEAKS\" ]; then
                echo \"  LEAK in \$(basename \$f):\"
                echo \"\$LEAKS\" | head -5 | sed 's/^/    /'
                LEAKS_FOUND=1
            fi
        done
        [ \$LEAKS_FOUND -eq 0 ] || { echo '  Error: developer paths leaked into release binaries -- aborting'; exit 1; }
        echo '  All binaries clean'
        # UPX only for Linux: UPX-packed Mach-O binaries are unreliable on
        # modern macOS (notably arm64) and trip Gatekeeper, and UPX-packed
        # Windows binaries are routinely flagged by antivirus, so ship the
        # macOS and Windows binaries uncompressed.
        echo '  Compressing Linux binaries with UPX...'
        if command -v upx >/dev/null 2>&1; then
            upx --best --lzma dist/${BINARY}-linux-amd64 || true
            upx --best --lzma dist/${BINARY}-linux-arm64 || true
        else
            echo '  UPX not available, skipping compression'
        fi
    "

echo ""
for f in dist/${BINARY}-*; do
    echo "  $(basename "$f"): $(du -h "$f" | cut -f1 | xargs)"
done
echo ""
echo "Built $(ls dist/${BINARY}-* 2>/dev/null | wc -l | tr -d ' ') binaries"

# --- Generate checksums ---
echo ""
echo "=== Generating checksums ==="
(cd dist && $SHA256CMD -- * > checksums.txt)
echo "  Generated checksums for $(wc -l < dist/checksums.txt | tr -d ' ') files"
cat dist/checksums.txt | sed 's/^/  /'

# --- Update VERSION file, commit, tag, push ---
echo ""
echo "=== Tagging and pushing ==="
echo "$NEW_VERSION" > "$VERSION_FILE"
git add "$VERSION_FILE"
git commit -m "Release $TAG"
echo "  Committed version bump to $NEW_VERSION"

git tag -a "$TAG" -m "Release $TAG"
echo "  Created tag $TAG"

git push origin master
git push origin "$TAG"
echo "  Pushed commit and tag to origin"

# --- Create GitHub release ---
echo ""
echo "=== Creating GitHub release ==="

RELEASE_BODY="## Mutiauk $TAG

### Binaries
- \`mutiauk-linux-amd64\` -- Linux x86_64
- \`mutiauk-linux-arm64\` -- Linux ARM64
- \`mutiauk-darwin-amd64\` -- macOS Intel
- \`mutiauk-darwin-arm64\` -- macOS Apple Silicon
- \`mutiauk-windows-amd64.exe\` -- Windows x86_64
- \`mutiauk-windows-arm64.exe\` -- Windows ARM64

The Windows binaries bundle the signed Wintun driver, so the .exe is
self-contained.

### Verification
- \`checksums.txt\` -- SHA256 checksums of all artifacts

### Quick Start (Linux)
\`\`\`bash
chmod +x mutiauk-linux-amd64
sudo mv mutiauk-linux-amd64 /usr/local/bin/mutiauk
sudo mutiauk setup
\`\`\`

### Quick Start (macOS)
The macOS binaries are unsigned, so clear the download quarantine first:
\`\`\`bash
chmod +x mutiauk-darwin-arm64
xattr -d com.apple.quarantine mutiauk-darwin-arm64 2>/dev/null || true
sudo mv mutiauk-darwin-arm64 /usr/local/bin/mutiauk
sudo mutiauk setup
\`\`\`

### Quick Start (Windows)
Run from an elevated (Administrator) PowerShell prompt:
\`\`\`powershell
.\\mutiauk-windows-arm64.exe setup
.\\mutiauk-windows-arm64.exe service install -c \"C:\\ProgramData\\Mutiauk\\config.yaml\"
\`\`\`"

create_release() {
    curl -s -X POST \
        "/repos//releases" \
        -H "Authorization: Bearer " \
        -H "Content-Type: application/json" \
        -d "$(jq -n \
            --arg tag "$TAG" \
            --arg name "Mutiauk $TAG" \
            --arg body "$RELEASE_BODY" \
            '{tag_name: $tag, name: $name, body: $body, draft: false, prerelease: false}'
        )"
}

# Retry release creation (GitHub may need a moment after first push)
RELEASE_ID=""
for attempt in 1 2 3; do
    RELEASE_RESPONSE=$(create_release)
    RELEASE_ID=$(echo "$RELEASE_RESPONSE" | jq -r '.id')
    if [ "$RELEASE_ID" != "null" ] && [ -n "$RELEASE_ID" ]; then
        break
    fi
    if [ "$attempt" -lt 3 ]; then
        echo "  Attempt $attempt failed, retrying in 3s..."
        sleep 3
    fi
done

if [ "$RELEASE_ID" = "null" ] || [ -z "$RELEASE_ID" ]; then
    echo "Error: Failed to create release after 3 attempts"
    echo "$RELEASE_RESPONSE" | jq .
    exit 1
fi
echo "  Created release ID: $RELEASE_ID"

# --- Upload assets ---
echo ""
echo "=== Uploading release assets ==="

upload_asset() {
    local file="$1"
    local name
    name=$(basename "$file")
    echo -n "  Uploading $name... "
    local response
    response=$(curl -s -X POST \
        "/repos//releases//assets?name=${name}" \
        -H "Authorization: Bearer " \
        -H "Content-Type: application/octet-stream" \
        --data-binary "@${file}")
    local asset_id
    asset_id=$(echo "$response" | jq -r '.id')
    if [ "$asset_id" = "null" ] || [ -z "$asset_id" ]; then
        echo "FAILED"
        echo "$response" | jq .
        return 1
    fi
    local size
    size=$(echo "$response" | jq -r '.size')
    echo "OK ($(numfmt --to=iec "$size" 2>/dev/null || echo "${size} bytes"))"
}

for file in dist/*; do
    upload_asset "$file"
done

# --- Verify release ---
echo ""
echo "=== Verifying release ==="
VERIFY=$(curl -s "/repos//releases/tags/${TAG}" \
    -H "Authorization: Bearer ")
ASSET_COUNT=$(echo "$VERIFY" | jq '.assets | length')
echo "  Release: $(echo "$VERIFY" | jq -r '.name')"
echo "  Tag:     $(echo "$VERIFY" | jq -r '.tag_name')"
echo "  Assets:  $ASSET_COUNT"
echo "$VERIFY" | jq -r '.assets[] | "  - \(.name) (\(.size) bytes)"'

echo ""
echo "=== Release $TAG complete ==="
echo "URL: https://github.com/${REPO}/releases/tag/${TAG}"
