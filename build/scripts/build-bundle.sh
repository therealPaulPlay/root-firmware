#!/bin/bash
# Build RAUC update bundle from rootfs image
#
# Usage: ./build-bundle.sh <rootfs.img> <version> <signing.key.pem> <signing.cert.pem> [output.raucb]
#
# Prerequisites: rauc must be installed

set -e

ROOTFS_IMG="$1"
VERSION="$2"
SIGNING_KEY="$3"
SIGNING_CERT="$4"
OUTPUT="${5:-root-observer-update-${VERSION}.raucb}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_DIR="$SCRIPT_DIR/../rauc"
BUILD_DIR=$(mktemp -d)

cleanup() {
    rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

# Validate inputs
if [ -z "$ROOTFS_IMG" ] || [ -z "$VERSION" ] || [ -z "$SIGNING_KEY" ] || [ -z "$SIGNING_CERT" ]; then
    echo "Usage: $0 <rootfs.img> <version> <signing.key.pem> <signing.cert.pem> [output.raucb]"
    exit 1
fi

if [ ! -f "$ROOTFS_IMG" ]; then
    echo "Error: Rootfs image not found: $ROOTFS_IMG"
    exit 1
fi

if [ ! -f "$SIGNING_KEY" ]; then
    echo "Error: Signing key not found: $SIGNING_KEY"
    exit 1
fi

if [ ! -f "$SIGNING_CERT" ]; then
    echo "Error: Signing certificate not found: $SIGNING_CERT"
    exit 1
fi

echo "Building RAUC bundle for version $VERSION..."

# Copy rootfs to build directory
echo "  Copying rootfs image..."
cp "$ROOTFS_IMG" "$BUILD_DIR/rootfs.img"

# Generate manifest from template
echo "  Generating manifest..."
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

sed -e "s/\${VERSION}/${VERSION}/g" \
    -e "s/\${BUILD_DATE}/${BUILD_DATE}/g" \
    "$TEMPLATE_DIR/manifest.raucm.template" > "$BUILD_DIR/manifest.raucm"

echo "  Manifest contents:"
cat "$BUILD_DIR/manifest.raucm"

# Create signed RAUC bundle
echo "  Creating signed bundle..."
rauc bundle \
    --cert="$SIGNING_CERT" \
    --key="$SIGNING_KEY" \
    "$BUILD_DIR" \
    "$OUTPUT"

# Show bundle info (--no-verify since we don't have or need the CA cert here)
echo ""
echo "Bundle created: $OUTPUT"
echo "Bundle info:"
rauc info --no-verify "$OUTPUT"

# Calculate and display hash
SHA256=$(sha256sum "$OUTPUT" | cut -d' ' -f1)
SIZE=$(stat -c%s "$OUTPUT" 2>/dev/null || stat -f%z "$OUTPUT")
echo ""
echo "SHA256: $SHA256"
echo "Size: $SIZE bytes"
