package tools

// UIDecl 描述工具的 UI 能力（通用 UI 元数据，任何工具/插件/MCP 都可声明）。
//
// 设计原则（见 docs/agent/genui-plugin-design.md §9）：UI 能力由工具元数据声明，
// 不由工具名决定。engine_wire 的流式提取、前端渲染判定全部读 UIDecl，
// 主仓库无任何硬编码工具名（如 display_html）。
type UIDecl struct {
	// Mode 声明 UI 模式。当前支持 "genui"（TSX 自由代码，前端 iframe 沙箱
	// + XBOT_UI 运行时渲染）；未来可扩展 "markdown" / "html" / "chart"。
	Mode string `json:"mode,omitempty"`
	// Param 承载 UI 代码的参数名（流式提取的目标字段）。
	// 例：display_html 工具的 code 参数 → Param="code"。
	Param string `json:"param,omitempty"`
	// Libs 提示前端注入的全局库（按需懒加载，不声明则不加载）。
	// 取值：echarts / three / motion（framer-motion 子集）。
	Libs []string `json:"libs,omitempty"`
	// Surface 声明 UI 结果作为「顶层面板」展示的形态（summary/标题特殊处理）。
	// nil = inline 默认（不特殊处理）。非 nil 时前端把它渲染成顶层面板：
	// 标题栏（summary 标题 + 折叠 + 全屏）+ 内容区，默认展开、可手动折叠/全屏。
	Surface *UISurface `json:"surface,omitempty"`
}

// UISurface 是通用的「顶层面板」展示声明，与 Mode 解耦（任何 UI 模式的工具
// 都可声明）。它让插件把自己产生的 UI 结果标记为需要特殊展示的「顶层元素」，
// 而非被自动折叠进普通工具列表。
type UISurface struct {
	// Kind 面板类型（当前支持 "panel"）。
	Kind string `json:"kind,omitempty"`
	// Title 面板标题（可选；缺省前端回退到工具的 Summary）。
	Title string `json:"title,omitempty"`
	// Collapsible 支持手动折叠（true 时标题栏出现折叠按钮）。
	Collapsible bool `json:"collapsible,omitempty"`
	// Fullscreen 支持全屏放大（true 时标题栏出现全屏按钮）。
	Fullscreen bool `json:"fullscreen,omitempty"`
	// DefaultOpen 默认展开（不自动折叠）。true = 初始展开。
	DefaultOpen bool `json:"default_open,omitempty"`
}

// UIDeclProvider 可选接口：工具实现它即声明 UI 能力。
// 返回 nil 表示该工具无 UI 能力（等价于未实现此接口）。
// 任何 tools.Tool 实现（内置工具 / ChannelToolBridge / PluginToolBridge / MCP）
// 都可实现此接口获得 UI 能力，消费方（engine_wire 流式提取、前端渲染判定）
// 只做类型断言读取，不依赖具体工具类型。
type UIDeclProvider interface {
	UIDecl() *UIDecl
}
