---
title: "xbot.ambience —— Web 氛围层"
weight: 3
---

# xbot.ambience

**script 运行时**插件（纯 UI），为 Web 界面添加氛围层：壁纸、毛玻璃表面、
动态桌宠挂件与粒子特效。

| | |
|--|--|
| **插件 ID** | `xbot.ambience` |
| **运行时** | `script`（无二进制，仅 manifest + Web 资产） |
| **权限** | `ui` |
| **贡献点** | `ambience`（壁纸、覆盖层）、挂件、用户设置 |

## 提供的能力

- **壁纸** —— CSS 渐变预设 + 用户上传（存在浏览器本地 IndexedDB
  `xbot-ambience`，上传时压缩到 ≤1600px）。
- **毛玻璃表面** —— 插件用 `color-mix(...)` 覆盖应用的 `--bg-*` CSS 变量；
  透明度与模糊度可调（模糊默认 0，避免逐帧 GPU 重合成）。
- **桌宠** —— 情绪跟随 Agent 生命周期：
  `turn.started → thinking`、`progress.iteration → working`、
  `turn.ended → done | sad`、闲置 10 分钟 → `sleeping`。
- **粒子** —— 可选星尘覆盖层（默认关闭）。

## 配置

**设置 → 外观 → 氛围** —— 选择壁纸、调整毛玻璃透明度/模糊度，可开启
**按会话独立配置**（每个会话用不同壁纸）。

## 说明

- 上传的资产**只存在浏览器本地**（IndexedDB）；配置本身经 `user_settings`
  同步，因此在其他设备上会回落到插件预设，直到你在该设备重新上传。
- 纯 UI 贡献：不注册任何工具，也不需要通道激活。

## 另见

- [插件系统总览](/zh-cn/plugins/)
- [Web 插件系统](/zh-cn/plugins/web/)
