---
title: "Privacy Policy"
weight: 80
---

# Privacy Policy

**Last updated: 2026-10-26** · Applies to all xbot clients (HarmonyOS app, Web UI, CLI) and the xbot backend.

## TL;DR

xbot is **self-hosted software**. The maintainers **run no servers, expose no backend API, and collect no data**.
Your data lives only on **your device** and in **the xbot instance you deploy yourself**.
The software is **MIT-licensed** — audit every line.

## 1. What we do NOT collect

| Category | Collected? |
|---|---|
| Account / phone / email / identity | ❌ no account system at all |
| Device identifiers, advertising ID, IMEI/OAID | ❌ |
| Location | ❌ |
| Analytics / behavioural tracking / A-B testing | ❌ |
| Crash reports / telemetry / performance uploads | ❌ |
| Conversations, files, shell output, model API keys | ❌ nothing is uploaded to us |
| Health data | ❌ never uploaded (see §4) |

> There is **no** "xbot cloud", no relay, no API gateway and no official account.
> The server URL you enter in the app must point to **your own** deployment.

## 2. Where data actually lives

| Data | Location |
|---|---|
| Server URL + access token | Your device (local preferences) |
| Sessions, attachments, tool results, plugin data | **Your** xbot instance (your server or laptop) |
| Model requests | The model provider **you** configured (OpenAI / Anthropic / self-hosted vLLM …), under their terms |
| Health data (optional) | Your device + your own instance — see §4 |

The maintainers cannot access any of it.

## 3. ⚠️ Do not trust somebody else's server

**Whoever operates an xbot instance can read every conversation, file and tool execution (including shell output) that goes through it.**

- ✅ Only connect to instances **you deployed**;
- ⛔ Never log into a stranger's "public xbot server" with real data;
- ⚠️ For team instances, assume the operator can see everything;
- We take no responsibility for third-party instances.

## 4. Health data (HarmonyOS Health Service Kit)

The app reads the data types you explicitly select **in the system permission dialog** (e.g. steps, heart rate, sleep, workouts, body measurements).

- **Purpose, limited to two things**: (a) showing **your own** trends inside the app; (b) when *you* ask a question, passing the relevant data to **the model you configured** to answer it;
- **Never** used for medical diagnosis, advertising, profiling, credit or insurance decisions; **never** shared or sold;
- **Not stored on maintainer servers** (there are none) — only on your device and your instance;
- **Revoke anytime** in *Settings → Privacy → Permission manager*; the app stops reading immediately. Delete history from your instance's data directory to erase it;
- Uninstalling the app or deleting the instance's data directory removes the data permanently — we keep no copy.

## 5. Permissions

| Permission | Why |
|---|---|
| `ohos.permission.INTERNET` | Talk to **your** xbot instance |
| `ohos.permission.GET_NETWORK_INFO` | Detect offline state |
| `ohos.permission.VIBRATE` | Haptic notification |
| Health Kit (optional) | See §4 — the app works without it |

## 6. Children

Not intended for children under 13. Instance operators are responsible for their own jurisdiction's compliance.

## 7. Changes

Every change to this policy lands as a Git commit in the repository (diff-auditable). Material changes are announced in release notes.

## 8. Contact

- Repository / issues: <https://github.com/ai-pivot/xbot>
- Security: see `SECURITY.md`

> We **cannot** handle "delete my data" requests — **we hold none**. Delete it on your device and in your own instance.

## 9. License

xbot is released under the **MIT License**: use, modify and redistribute freely (including commercially) while keeping the copyright notice; provided **"AS IS"**, without warranty of any kind.
