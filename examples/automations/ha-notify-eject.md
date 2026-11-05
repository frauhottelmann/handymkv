# ha-notify-eject (Example Automation)

Notifies [Home Assistant](https://www.home-assistant.io/) through a webhook trigger when HandyMKV has finished, then ejects the disc. Two versions are included:

- `ha-notify-eject.ps1` for Windows (Windows PowerShell 5.1 or PowerShell 7)
- `ha-notify-eject.sh` for Linux and macOS (needs `curl` and `eject` / `drutil`)

## What it does

1. Sends a JSON `POST` to the webhook URL with data from the run.
2. Ejects the optical drive.

The disc is ejected even if the webhook call fails. If either step fails, the script exits with code 1 so HandyMKV shows the automation as `FAILED`.

> **Timing:** HandyMKV runs automations after *all* titles have been ripped **and** encoded, so the disc is ejected at the end of the run, not right after ripping.

## Parameters

| Name | Source | Description |
|------|--------|-------------|
| `webhook_url` | `static` | Full webhook URL, e.g. `http://homeassistant.local:8123/api/webhook/handymkv_done`. If left empty, the script reads the `HA_WEBHOOK_URL` environment variable instead. |
| `drive` | `static` | Windows: drive letter of the optical drive, e.g. `E:`. Linux: device such as `/dev/sr0` (empty = default drive). Ignored on macOS. |
| `encoded_dir` | `hmkv_output` (key: `hb_output_dir`) | Optional, sent in the payload. |
| `title_count` | `hmkv_output` (key: `title_count`) | Optional, sent in the payload. |
| `run_duration` | `hmkv_output` (key: `run_duration`) | Optional, sent in the payload. |
| `total_encoded_size` | `hmkv_output` (key: `total_encoded_size`) | Optional, sent in the payload (bytes). |

Payload sent to Home Assistant (optional values are empty strings when not configured):

```json
{
  "event": "handymkv_finished",
  "drive": "E:",
  "encoded_dir": "D:\\Video",
  "title_count": "3",
  "run_duration": "42m10s",
  "total_encoded_size": "4831838208",
  "computer": "MY-PC"
}
```

## Setup

### 1. Home Assistant

Create an automation with a webhook trigger. The webhook ID becomes the last part of the URL. Example:

```yaml
alias: HandyMKV finished
triggers:
  - trigger: webhook
    webhook_id: handymkv_done
    allowed_methods: [POST]
    local_only: true
actions:
  - action: notify.notify
    data:
      title: HandyMKV
      message: >-
        {{ trigger.json.title_count }} title(s) done in {{ trigger.json.run_duration }},
        disc ejected from {{ trigger.json.drive }}.
```

Treat the webhook ID like a password: anyone who knows it can trigger the automation. Keep `local_only: true` unless HandyMKV runs outside your network.

### 2. HandyMKV

```shell
handymkv automations create
```

- **Name**: `ha-notify-eject`
- **Command**:
  - Windows: `powershell -NoProfile -ExecutionPolicy Bypass -File C:\path\to\ha-notify-eject.ps1`
  - Linux/macOS: `/path/to/examples/automations/ha-notify-eject.sh`
- **Param 1**: name `webhook_url`, source `static`, value `http://homeassistant.local:8123/api/webhook/handymkv_done`
- **Param 2**: name `drive`, source `static`, value `E:` (Windows) or `/dev/sr0` (Linux)
- **Param 3** (optional): name `title_count`, source `hmkv_output`, key `title_count`
- **Param 4** (optional): name `run_duration`, source `hmkv_output`, key `run_duration`
- More optional params: `encoded_dir` (`hb_output_dir`), `total_encoded_size` (`total_encoded_size`)

> **Windows note:** HandyMKV runs the command through `cmd /c`. Put the script in a path **without spaces** and don't wrap it in quotes, otherwise `cmd` may not parse the command correctly.

Then run:

```shell
handymkv -a ha-notify-eject
```

## Testing

You can test the script without a rip by setting the variables yourself.

Windows (PowerShell):

```powershell
$env:HMKV_PARAM_WEBHOOK_URL = 'http://homeassistant.local:8123/api/webhook/handymkv_done'
$env:HMKV_PARAM_DRIVE = 'E:'
powershell -NoProfile -ExecutionPolicy Bypass -File C:\path\to\ha-notify-eject.ps1
```

Linux/macOS:

```shell
HMKV_PARAM_WEBHOOK_URL=http://homeassistant.local:8123/api/webhook/handymkv_done \
HMKV_PARAM_DRIVE=/dev/sr0 ./ha-notify-eject.sh
```
