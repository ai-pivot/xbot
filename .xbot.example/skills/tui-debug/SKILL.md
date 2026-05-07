# TUI Debug Skill — Scenario-Driven TUI Simulator

零依赖场景驱动 TUI 模拟器。35+ action types，16+ assertion types。3126 行代码，31 测试，3 benchmark，7 回归场景。

## 最简用法

```bash
# 编译
cd /home/user/src/xbot && go test -c -o /tmp/xbot-tui-sim ./channel/

# 管道输入（最快）
echo '{"config":{"width":120,"height":40},"steps":[
  {"action":"turn","content":"hello","response":"Hi!"},
  {"action":"assert","assert_role":"assistant","assert_count":1}
]}' | ~/.xbot/skills/tui-debug/scripts/run-sim.sh -

# 文件输入
XBOT_SIM_SCENARIO=scene.json /tmp/xbot-tui-sim -test.run TestSimMain

# 安静模式（一行输出）
XBOT_SIM_QUIET=1 XBOT_SIM_SCENARIO=scene.json /tmp/xbot-tui-sim -test.run TestSimMain

# 完整报告（JSON+MD）
XBOT_SIM_SCENARIO=s.json XBOT_SIM_OUTPUT=r.json /tmp/xbot-tui-sim -test.run TestSimMain
```

## 输出模式

| 环境变量 | 输出 |
|---------|------|
| (默认) | Markdown 到 stdout |
| `XBOT_SIM_OUTPUT=r.json` | JSON + 自动 .md 文件 |
| `XBOT_SIM_QUIET=1` | 单行状态 |
| `XBOT_SIM_TRACE=1` | 含步骤 trace log |

## Action 速查 (35+)

**消息**: `user_msg` `agent_msg` `system_msg` `turn`(快捷)
**Progress**: `progress` `phase_done` `subagent` `cancel`
**控制**: `key` `resize` `rewind` `clear` `tick` `set_var` `queue_add` `scroll` `input_text`
**观测**: `snapshot` `inspect` `summary` `diff` `export` `loop` `include` `if/then/else` `comment` `validate` `capture_history` `help`

## 断言速查

### View
`contains` `not_contains` `matches` `visible_contains` `visible_not_contains`
`assert_view_lines` `assert_view_lines_max` `assert_viewport_at_bottom/top`

### Message
`assert_role` + `assert_count`/`assert_content`/`assert_content_regex`/`assert_tools`
`assert_index` + `assert_index_role`/`assert_content`
`assert_message_order` `assert_total`

### Tool
`assert_no_tool_errors` `assert_tool_call_count` `assert_tool_name` + `min_ms`/`max_ms`

### State
`assert_state: {"typing": false, "messageCount": 2}`

## 最佳实践
- 优先 message-level 断言
- 用 `turn` 减少冗余
- 用 `validate` 验证场景
- 用 `summary` 快速查看
- 用 `export` + `history` 复用
- 用 `visible_contains` 测试滚动

## 回归测试
```bash
for f in channel/testdata/scenarios/*.json; do
  XBOT_SIM_QUIET=1 XBOT_SIM_SCENARIO=$f /tmp/xbot-tui-sim -test.run TestSimMain
done
```

## 实现
- `channel/cli_sim_test.go` — 3126行, 31测试, 3 benchmark
- `channel/testdata/scenarios/` — 7 回归场景
- `~/.xbot/skills/tui-debug/scripts/` — run-sim.sh (支持管道/文件/trace/output)
