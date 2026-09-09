---
title: "Multimodal Vision"
weight: 35
---

# Multimodal Vision Input

xbot sends images into the multimodal model's actual visual context — user uploads, Feishu message images, and images the model pulls in itself via the `view_image` tool all flow through one pipeline.

{{< hint type=important >}}
The vision switch is a **purely manual per-model setting** — there is **no built-in model-name whitelist**. Each model has its own toggle under Settings → LLM → model editor; you decide which models accept images.
{{< /hint >}}

## Enabling Vision

1. Open **Settings → LLM console**, expand a subscription and find the target model row
2. Click the model's edit button to open the model config
3. Turn on **Vision (multimodal input)**
4. (Optional) pick a detail level: **Auto / Low (fewer tokens) / High (more detail)** — the OpenAI `image_url.detail` hint; low is ~85 tokens per image, high ~765+
5. Save

Vision-enabled models show a **👁 vision** badge in the model list (the CLI model panel `Ctrl+N` shows 👁 too).

Models with vision **off** never fail on image messages: images degrade to `[image: name — vision input not enabled]` text placeholders. The model knows an image was sent but cannot see it, and will tell the user to enable vision or switch models.

## Uploading Images (Web)

- **Paste / drop / pick**: pasting a screenshot or dropping an image file attaches it (with a progress chip) and inserts an inline preview in the composer
- Uploaded images render as thumbnails in the message; **click to open the lightbox** (fullscreen + open original)
- When the current model has vision **off**, an amber advisory bar appears above the composer (non-blocking — the message still sends with name-only placeholders)

### Image budget

At most **8 images** enter the visual context per request (most recent win; older ones degrade to text placeholders). Images are preprocessed server-side before sending: longest edge scaled to **≤ 2048px**, squeezed to **≤ 4MB** (jpeg re-encode). The original file is untouched — only the copy sent to the model is optimized.

## view_image tool (the model looks at its own images)

The model can call the `view_image` tool to put an image from its environment into its own context:

- Analyze a chart Python just generated, inspect a downloaded picture, compare screenshots
- Parameters: `path` (workspace-local file) or `url` (http/https image)
- The tool reads, stores, and injects the image into the next turn's visual context (shown in the message flow as a 📷 injected image)

```
User: check whether chart.png looks right
Model: (calls view_image path=chart.png)
📷 image loaded via the view_image tool
Model: the Y-axis has 3 duplicated ticks...
```

## Feishu images

Sending an image in a Feishu chat: xbot downloads and injects it into the visual context (stored in the persistent view_images directory) — the same pipeline as web uploads. Download failures degrade to a file tag (the message is never lost).

## Supported formats

png / jpeg / gif / webp / bmp / tiff (bmp and tiff auto-convert to png; gif passes through untouched — animation preserved). Rare formats (heic etc.) pass through as-is and are up to the API to accept.

## History replay

Image references are **relative URLs** (signed on every access) — images in history **never expire** (very old messages from before this feature show a "failed to load" placeholder with the original link, by design).

## How it works (short)

```
content stores stable references (~100B markdown)
    ↓ at request-build time
llm layer resolves refs → ImageResolver → base64 data URL (LRU cache, 32 / 128MB)
    ↓ vision enabled
OpenAI image_url parts / Anthropic base64 image blocks
    ↓ vision off
[image: name — vision input not enabled] (text placeholder, request never fails)
```

- Image token cost comes from the API's real `prompt_tokens` (the context bar and compression decisions are naturally correct)
- The same image reuses the LRU cache across turns — no repeat downloads
- Compression / history / frontend pipelines only ever see the small string references
