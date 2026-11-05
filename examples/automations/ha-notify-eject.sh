#!/bin/sh
#
# ha-notify-eject.sh
#
# Notifies Home Assistant via webhook, then ejects the disc.
# The disc is ejected even if the webhook call fails; the script
# exits non-zero if either step failed.
#
# HandyMKV automation parameters (environment variables):
#   HMKV_PARAM_WEBHOOK_URL         - Full webhook URL (static). Falls back to HA_WEBHOOK_URL.
#   HMKV_PARAM_DRIVE               - Optical drive device, e.g. /dev/sr0 (static, Linux).
#                                    Defaults to the default drive if empty. Ignored on macOS.
#   HMKV_PARAM_ENCODED_DIR         - Optional (hmkv_output: hb_output_dir)
#   HMKV_PARAM_TITLE_COUNT         - Optional (hmkv_output: title_count)
#   HMKV_PARAM_RUN_DURATION        - Optional (hmkv_output: run_duration)
#   HMKV_PARAM_TOTAL_ENCODED_SIZE  - Optional (hmkv_output: total_encoded_size)
#
# Requires: curl, and eject (Linux) or drutil (macOS).

failed=0
url="${HMKV_PARAM_WEBHOOK_URL:-${HA_WEBHOOK_URL:-}}"
drive="${HMKV_PARAM_DRIVE:-}"

# Escape a value for use inside a JSON string
json_escape() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# --- 1. Notify Home Assistant ---
if [ -z "$url" ]; then
    echo "Error: no webhook URL (set HMKV_PARAM_WEBHOOK_URL or HA_WEBHOOK_URL)."
    failed=1
else
    payload=$(printf '{"event":"handymkv_finished","drive":"%s","encoded_dir":"%s","title_count":"%s","run_duration":"%s","total_encoded_size":"%s","computer":"%s"}' \
        "$(json_escape "$drive")" \
        "$(json_escape "${HMKV_PARAM_ENCODED_DIR:-}")" \
        "$(json_escape "${HMKV_PARAM_TITLE_COUNT:-}")" \
        "$(json_escape "${HMKV_PARAM_RUN_DURATION:-}")" \
        "$(json_escape "${HMKV_PARAM_TOTAL_ENCODED_SIZE:-}")" \
        "$(json_escape "$(hostname)")")
    if curl -fsS -m 15 -X POST -H 'Content-Type: application/json' -d "$payload" "$url" >/dev/null; then
        echo "Home Assistant notified."
    else
        echo "Error: webhook call failed."
        failed=1
    fi
fi

# --- 2. Eject the disc ---
eject_ok=0
case "$(uname -s)" in
    Darwin)
        drutil eject && eject_ok=1
        ;;
    *)
        if [ -n "$drive" ]; then eject "$drive" && eject_ok=1; else eject && eject_ok=1; fi
        ;;
esac
if [ $eject_ok -eq 1 ]; then
    echo "Disc ejected."
else
    echo "Error: could not eject the disc."
    failed=1
fi

exit $failed
