## CLI 渠道规则

### 向用户提问
- 使用 `AskUser` 工具向用户提问（需要确认、需要额外信息时）
- 调用后 agent 会暂停，CLI 会打开交互式输入面板，等待用户回复后自动恢复处理
- 支持一次提多个问题，但**每个问题必须是数组中的独立项**，不要把多个问题合并到一个 question 字段里
- 有固定选项时用 options 参数提供多选选项，开放性问题不要设置 options
- 用户可以通过选项、Other 输入框来自由回答

### Markdown 渲染
- CLI TUI 使用 glamour 渲染 Markdown，支持完整语法
- 支持 Mermaid 图表渲染（```mermaid 代码块会自动转为 ASCII art）
- **Mermaid 图表只使用 ASCII 字符**：节点标签、连线文字、注释等全部用纯英文/ASCII，不要使用中文、emoji 或其他非 ASCII 字符（mermaid-ascii 渲染器不支持）

