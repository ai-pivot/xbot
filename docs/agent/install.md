# xbot 安装（Agent 入口）

> **本文件是指针，不是完整文档。** 完整可执行版（每步断言 + 排错表）只有一个维护点：
>
> - 公开页（推荐，agent 直接 fetch）：<https://ai-pivot.github.io/xbot/agent-install/>
> - 中文同上：<https://ai-pivot.github.io/xbot/zh-cn/agent-install/>
>
> 以前这里是一份完整拷贝，会和公开页漂移（2026-09-11 发现 3 处已经过期：systemd 服务名写成
> `xbot.service`、手工建 unit 其实多余、`xbot-cli serve --install-service` 这个 flag 根本不存在）。
> 因此只保留下面的最小事实集。

## 安装与自检（最小闭环）

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
xbot-cli setup --check      # 唯一完整性判据：退出码 0 = 完整；1 = 缺件 -> xbot-cli setup
xbot-cli serve              # 端口取 config.json 的 web.port（默认 8082）
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/   # 必须 200
```

中国大陆：`curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash`

## Agent 最容易踩的 4 件事

1. **`setup --check` 是唯一判据** —— 不看退出码就报"装好了"是不允许的。
2. **LLM 配置在数据库里**（`user_llm_subscriptions`），`config.json` 的 `llm` 段只是首次启动的
   播种来源。装完再改 `config.json` **不生效**（要按公开页 4b 清掉已播种订阅才重新播种）。
3. **服务名是 `xbot-server`**（systemd --user），由 `MODE=server-client` 安装时自动写好并启用；
   `xbot-cli serve` **没有** `--install-service` 之类的 flag（它只接受 `--config`）。
4. **回复文本在 `iteration_history`，不在 `session_messages.content`**（v55 起后者是空占位行）。

## 脚本自带的入口

`scripts/install.sh` 的头部注释里有一段 `AI AGENTS — READ THIS`（下载脚本再读时可见），
运行结束时也会打印同样的下一步提示 + 公开文档 URL（`curl | bash` 也能看到）。
改安装流程时两处要同步更新。
