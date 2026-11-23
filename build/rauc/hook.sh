#!/bin/bash
# RAUC bundle hook
# Selects the device's hardware variant config on the installed boot partition

set -e

# Written at image creation, never changed by updates
VARIANT_FILE="/boot/firmware/variant"

case "$1" in
    slot-post-install)
        [ "$RAUC_SLOT_CLASS" = "boot" ] || exit 0

        # Abort install if no matching variant is provided
        variant=$(cat "$VARIANT_FILE" 2>/dev/null | tr -d '[:space:]')
        if [ -z "$variant" ]; then
            echo "No hardware variant in $VARIANT_FILE" >&2
            exit 1
        fi

        variant_config="$RAUC_SLOT_MOUNT_POINT/variants/${variant}.txt"
        if [ ! -f "$variant_config" ]; then
            echo "Variant config variants/${variant}.txt not found in the update" >&2
            exit 1
        fi

        # config.txt includes variant.txt
        cp "$variant_config" "$RAUC_SLOT_MOUNT_POINT/variant.txt"
        sync
        echo "Selected variant $variant for $RAUC_SLOT_DEVICE"
        ;;
esac
