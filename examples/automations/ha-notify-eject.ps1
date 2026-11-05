<#
.SYNOPSIS
    HandyMKV automation: notifies Home Assistant via webhook, then ejects the disc.

.DESCRIPTION
    Sends a JSON POST to a Home Assistant webhook trigger with data from the
    HandyMKV run, then ejects the optical drive. The disc is ejected even if
    the webhook call fails; the script exits non-zero if either step failed.

    HandyMKV automation parameters (environment variables):
      HMKV_PARAM_WEBHOOK_URL         - Full webhook URL, e.g. http://homeassistant.local:8123/api/webhook/handymkv_done (static)
                                       Falls back to the HA_WEBHOOK_URL environment variable if not set.
      HMKV_PARAM_DRIVE               - Drive letter of the optical drive, e.g. E: (static)
      HMKV_PARAM_ENCODED_DIR         - Optional (hmkv_output: hb_output_dir)
      HMKV_PARAM_TITLE_COUNT         - Optional (hmkv_output: title_count)
      HMKV_PARAM_RUN_DURATION        - Optional (hmkv_output: run_duration)
      HMKV_PARAM_TOTAL_ENCODED_SIZE  - Optional (hmkv_output: total_encoded_size)

    Command to use in `handymkv automations create` (path without spaces):
      powershell -NoProfile -ExecutionPolicy Bypass -File C:\path\to\ha-notify-eject.ps1
#>

$ErrorActionPreference = 'Stop'
$failed = $false

$webhookUrl = $env:HMKV_PARAM_WEBHOOK_URL
if ([string]::IsNullOrWhiteSpace($webhookUrl)) { $webhookUrl = $env:HA_WEBHOOK_URL }

$drive = $env:HMKV_PARAM_DRIVE
if ([string]::IsNullOrWhiteSpace($drive)) {
    Write-Host 'Error: HMKV_PARAM_DRIVE is not set (e.g. E:).'
    exit 1
}
# Accept "E", "E:" and "E:\"
$drive = $drive.Trim().TrimEnd('\')
if ($drive -notmatch ':$') { $drive = "${drive}:" }
$drive = $drive.ToUpper()

# --- 1. Notify Home Assistant ---------------------------------------------
if ([string]::IsNullOrWhiteSpace($webhookUrl)) {
    Write-Host 'Error: no webhook URL (set HMKV_PARAM_WEBHOOK_URL or HA_WEBHOOK_URL).'
    $failed = $true
} else {
    $payload = [ordered]@{
        event              = 'handymkv_finished'
        drive              = $drive
        encoded_dir        = $env:HMKV_PARAM_ENCODED_DIR
        title_count        = $env:HMKV_PARAM_TITLE_COUNT
        run_duration       = $env:HMKV_PARAM_RUN_DURATION
        total_encoded_size = $env:HMKV_PARAM_TOTAL_ENCODED_SIZE
        computer           = $env:COMPUTERNAME
    }
    # Send UTF-8 bytes so paths with umlauts survive Windows PowerShell 5.1
    $body = [System.Text.Encoding]::UTF8.GetBytes(($payload | ConvertTo-Json -Compress))
    try {
        # Windows PowerShell 5.1 defaults to older TLS versions
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        Invoke-RestMethod -Uri $webhookUrl -Method Post -ContentType 'application/json; charset=utf-8' -Body $body -TimeoutSec 15 | Out-Null
        Write-Host 'Home Assistant notified.'
    } catch {
        Write-Host "Error: webhook call failed: $($_.Exception.Message)"
        $failed = $true
    }
}

# --- 2. Eject the disc ------------------------------------------------------
try {
    $item = (New-Object -ComObject Shell.Application).Namespace(17).ParseName($drive)
    if ($null -eq $item) { throw "drive $drive not found" }
    $item.InvokeVerb('Eject')
    Write-Host "Ejected disc in $drive."
} catch {
    Write-Host "Error: could not eject ${drive}: $($_.Exception.Message)"
    $failed = $true
}

if ($failed) { exit 1 }
exit 0
