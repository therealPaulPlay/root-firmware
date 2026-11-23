#!/bin/bash
# Build RAUC update bundle from rootfs and boot partition images
#
# Usage: ./build-bundle.sh <rootfs.ext4> <boot.vfat> <version> <signing.key.pem> <signing.cert.pem> <ca.cert.pem> [output.raucb]
#
# Prerequisites: rauc must be installed

set -e

ROOTFS_IMG="$1"
BOOT_IMG="$2"
VERSION="$3"
SIGNING_KEY="$4"
SIGNING_CERT="$5"
CA_CERT="$6"
OUTPUT="${7:-root-observer-update-${VERSION}.raucb}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE_DIR="$SCRIPT_DIR/../rauc"
BUILD_DIR=$(mktemp -d)
MIN_CERT_VALIDITY_DAYS=180

cleanup() {
    rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

# Validate inputs
if [ -z "$ROOTFS_IMG" ] || [ -z "$BOOT_IMG" ] || [ -z "$VERSION" ] || [ -z "$SIGNING_KEY" ] || [ -z "$SIGNING_CERT" ] || [ -z "$CA_CERT" ]; then
    echo "Usage: $0 <rootfs.ext4> <boot.vfat> <version> <signing.key.pem> <signing.cert.pem> <ca.cert.pem> [output.raucb]"
    exit 1
fi

if [ ! -f "$ROOTFS_IMG" ]; then
    echo "Error: Rootfs image not found: $ROOTFS_IMG"
    exit 1
fi

if [ ! -f "$BOOT_IMG" ]; then
    echo "Error: Boot image not found: $BOOT_IMG"
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

if [ ! -f "$CA_CERT" ]; then
    echo "Error: CA certificate not found: $CA_CERT"
    exit 1
fi

# Check signing certificate expiry
MIN_CERT_VALIDITY_SECONDS=$((MIN_CERT_VALIDITY_DAYS * 24 * 3600))
if ! openssl x509 -in "$SIGNING_CERT" -noout -checkend "$MIN_CERT_VALIDITY_SECONDS" > /dev/null; then
    echo "Error: Signing certificate expires within $MIN_CERT_VALIDITY_DAYS days, reissue it with the CA key"
    exit 1
fi

echo "Building RAUC bundle for version $VERSION..."

# Copy images and hook to build directory
echo "  Copying images and hook..."
cp "$ROOTFS_IMG" "$BUILD_DIR/rootfs.ext4"
cp "$BOOT_IMG" "$BUILD_DIR/boot.vfat"
install -m 755 "$TEMPLATE_DIR/hook.sh" "$BUILD_DIR/hook.sh"

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

# Show bundle info (verified against the CA cert)
echo ""
echo "Bundle created: $OUTPUT"
echo "Bundle info:"
rauc info --keyring="$CA_CERT" "$OUTPUT"

# Calculate and display hash
SHA256=$(sha256sum "$OUTPUT" | cut -d' ' -f1)
SIZE=$(stat -c%s "$OUTPUT" 2>/dev/null || stat -f%z "$OUTPUT")
echo ""
echo "SHA256: $SHA256"
echo "Size: $SIZE bytes"
