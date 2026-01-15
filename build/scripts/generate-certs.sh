#!/bin/bash
# Generate self-signed certificates for RAUC bundle signing
# Run once locally, then store in GitHub Secrets
#
# Usage: ./generate-certs.sh [output_directory]
#
# After running, add these secrets to your GitHub repository:
#   RAUC_CA_CERT       - Contents of ca.cert.pem
#   RAUC_SIGNING_CERT  - Contents of signing.cert.pem
#   RAUC_SIGNING_KEY   - Contents of signing.key.pem

set -e

OUTPUT_DIR="${1:-./certificates}"
VALIDITY_DAYS=3650  # 10 years
KEY_SIZE=4096

mkdir -p "$OUTPUT_DIR"

echo "Generating RAUC signing certificates..."

# Generate CA private key
echo "  Creating CA private key..."
openssl genrsa -out "$OUTPUT_DIR/ca.key.pem" $KEY_SIZE 2>/dev/null

# Generate CA certificate (self-signed)
echo "  Creating CA certificate..."
openssl req -new -x509 \
    -key "$OUTPUT_DIR/ca.key.pem" \
    -out "$OUTPUT_DIR/ca.cert.pem" \
    -days $VALIDITY_DAYS \
    -subj "/CN=ROOT Observer CA/O=ROOT Privacy/C=US" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign"

# Generate signing key
echo "  Creating signing key..."
openssl genrsa -out "$OUTPUT_DIR/signing.key.pem" $KEY_SIZE 2>/dev/null

# Generate signing certificate request
openssl req -new \
    -key "$OUTPUT_DIR/signing.key.pem" \
    -out "$OUTPUT_DIR/signing.csr.pem" \
    -subj "/CN=ROOT Observer Signing/O=ROOT Privacy/C=US"

# Sign the certificate with CA
echo "  Signing certificate with CA..."
openssl x509 -req \
    -in "$OUTPUT_DIR/signing.csr.pem" \
    -CA "$OUTPUT_DIR/ca.cert.pem" \
    -CAkey "$OUTPUT_DIR/ca.key.pem" \
    -CAcreateserial \
    -out "$OUTPUT_DIR/signing.cert.pem" \
    -days $VALIDITY_DAYS \
    -extfile <(printf "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature") \
    2>/dev/null

# Clean up intermediate files
rm -f "$OUTPUT_DIR/signing.csr.pem" "$OUTPUT_DIR/ca.srl"

echo ""
echo "Certificates generated in $OUTPUT_DIR:"
echo ""
echo "  ca.cert.pem       - CA certificate (PUBLIC - embed in device rootfs)"
echo "  ca.key.pem        - CA private key (KEEP SECURE - only for signing new certs)"
echo "  signing.cert.pem  - Signing certificate (for GitHub Secret: RAUC_SIGNING_CERT)"
echo "  signing.key.pem   - Signing private key (for GitHub Secret: RAUC_SIGNING_KEY)"
echo ""
echo "GitHub Secrets to create:"
echo ""
echo "  RAUC_CA_CERT      = contents of ca.cert.pem"
echo "  RAUC_SIGNING_CERT = contents of signing.cert.pem"
echo "  RAUC_SIGNING_KEY  = contents of signing.key.pem"
echo ""
echo "Important: Keep ca.key.pem secure and backed up. It's needed to issue new signing certificates."
echo ""

# Verify certificates
echo "Verifying certificate chain..."
openssl verify -CAfile "$OUTPUT_DIR/ca.cert.pem" "$OUTPUT_DIR/signing.cert.pem"
echo ""
echo "Certificate details:"
openssl x509 -in "$OUTPUT_DIR/signing.cert.pem" -noout -subject -issuer -dates
