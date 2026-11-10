---
title: "Terms of Use"
weight: 85
---

# Terms of Use (Software License & Usage Agreement)

**Last updated: 2026-10-26**

## 0. Read this first

xbot is **open-source software**, not an online service. The maintainers **provide no backend, no API and no hosting**.
You must **deploy the xbot backend yourself** (your server or laptop) and configure your own model provider and API keys.
These terms govern your use of the software; they are **not** a service-level commitment.

## 1. License

xbot is released under the **MIT License** (full text in `LICENSE`):

- ✅ You may use, copy, modify, merge, publish, distribute, sublicense and sell copies;
- 📌 Condition: keep the copyright and permission notices in copies or substantial portions;
- ⚠️ No warranty: the software is provided **"AS IS"**, without warranty of any kind.

## 2. You must self-host the backend

- Clients (HarmonyOS app / Web UI / CLI) are connectors — they need an xbot instance **you** deploy;
- The maintainers **do not provide**: hosted instances, public APIs, relays, accounts, storage, backup or recovery;
- Consequently, availability, backups, hardening and regulatory compliance are **your responsibility**;
- Model usage costs are billed by your provider, not by the maintainers.

## 3. ⚠️ Do not trust somebody else's server

**Any instance you connect to that you did not deploy can be read by its operator** — including conversations, files and shell output.
Never enter API keys, personal or business secrets into someone else's instance.
The maintainers accept no liability for third-party instances.

## 4. Your responsibilities

You agree to:

1. Not use the software for unlawful purposes (unauthorised access, malware distribution, privacy violations);
2. Comply with the terms of the model providers you configure;
3. If you use health-data features (HarmonyOS Health Service Kit), comply with Huawei's developer and health-data policies and process **only your own data, or data you have explicit consent to process**;
4. Accept the consequences of running the software (including data loss, unintended command execution and costs).

> ⚠️ xbot executes shell commands and reads/writes files. Only run it on machines you are authorised to control.

## 5. No warranty; limitation of liability

To the maximum extent permitted by law, the authors and contributors are **not liable** for any direct, indirect, incidental, special or consequential damages (including lost profits, lost data or business interruption), however caused and under any theory of liability, even if advised of the possibility of such damage.

## 6. Privacy

See the [Privacy Policy](/privacy/): **no data is collected by the maintainers**; data stays on your device and in your own instance.

## 7. Changes and termination

- These terms evolve with the repository; material changes are noted in release notes;
- You may stop using the software and delete it (and its data) at any time;
- If you violate §4, the maintainers may decline support (issues/PRs).

## 8. Governing law

These terms do not establish a service relationship in any jurisdiction; disputes relating to the software are governed by the warranty disclaimer and limitation of liability in the MIT License.

## 9. Contact

- Repository: <https://github.com/ai-pivot/xbot>
- Issues / Discussions on GitHub
