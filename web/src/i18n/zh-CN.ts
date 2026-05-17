/**
 * i18n Constants — zh-CN
 * Centralized Chinese text constants for the Web UI.
 * Extract all hardcoded user-facing strings here for future i18n support.
 */
const zhCN = {
  // App
  appName: 'xbot',
  appSubtitle: '智能对话助手',

  // Auth
  login: '登录',
  register: '注册',
  logout: 'Logout',
  username: '用户名',
  password: '密码',
  loginFailed: '登录失败',
  registerFailed: '注册成功但登录失败，请手动登录',
  networkError: '网络错误',
  operationFailed: '操作失败',
  enterUsername: '输入用户名',
  enterPassword: '输入密码',
  noAccount: '没有账号？注册',
  hasAccount: '已有账号？登录',
  createAccount: '创建账号',
  feishuLogin: '飞书登录',
  feishuLoginFailed: '飞书登录失败',
  feishuUserId: '飞书用户 ID',
  feishuPassword: '关联的 Web 账号密码',
  feishuBindHint: '需要先在飞书中绑定 Web 账号，使用绑定时设置的密码登录。',
  backToPasswordLogin: '← 返回账号密码登录',
  loginViaFeishu: '通过飞书用户 ID 登入',

  // Chat
  startConversation: '开始一段对话',
  sendFirstMessage: '发送消息开始与 AI 助手交流',
  searchHistory: '搜索历史消息',
  viewCommands: '查看快捷指令',
  inputPlaceholder: '输入消息... (Shift+Enter 换行)',
  connecting: '连接中...',
  send: '发送',
  sendMessage: '发送消息',
  stopGeneration: '停止生成',
  scrollToBottom: '滚动到底部',
  newMessages: '新消息',
  conversationCleared: '对话已清空',
  newConversation: '新对话',
  copyContent: '复制内容',
  copied: '✓ Copied',
  copyCode: '复制代码',

  // Status
  connected: '● Connected',
  connectingStatus: '◐ Connecting...',
  disconnected: '○ Disconnected',
  serverDisconnected: '⛔ 服务已断开，请刷新页面重新连接',
  reconnecting: '⚠️ 连接断开，正在尝试重连...',
  contextUsage: '上下文使用',

  // Settings
  settings: '⚙️ 设置',
  closeSettings: '关闭设置',
  saving: '保存中...',
  appearance: '外观',
  sessions: '会话',
  presets: '快捷指令',
  llm: 'LLM',
  runner: 'Runner',
  market: '市场',
  theme: '主题',
  dark: '深色',
  light: '浅色',
  fontSize: '字体大小',
  nickname: '昵称',
  switchModel: '切换模型',
  modelSelect: '模型选择',

  // Search
  searchMessages: '搜索消息历史...',
  searching: '搜索中...',
  noResults: '未找到匹配结果',

  // File upload
  uploadFile: '上传文件',
  uploadFailed: '上传失败',
  fileTooLarge: '文件超过 10MB 限制',
  uploadResponseError: '上传响应异常',
  dragToUpload: '拖拽文件到此处上传',
  dragSupportedTypes: '支持图片、文档、代码等文件',
  remove: '移除',

  // Commands
  cmdClear: '清空对话',
  cmdNew: '新对话',
  cmdCompact: '压缩上下文',
  cmdHelp: '显示帮助',
  cmdCancel: '取消当前生成',
  helpTitle: '可用命令',

  // AskUser
  agentNeedsInput: 'Agent 需要你的输入',
  inputAnswer: '输入你的回答...',
  submit: '提交',
  previousQuestion: '← 上一题',
  cancel: '取消',

  // Progress
  thinking: '思考中...',
  rendering: '渲染图表中...',

  // Mermaid
  mermaidRenderFailed: 'Mermaid 渲染失败',

  // Image / File
  image: '图片',
  file: '文件',

  // Confirm dialog
  confirm: '确定',

  // Sidebar
  searchPlaceholder: '搜索会话...',

  // Errors
  switchFailed: '切换失败',

  // Runner
  local: '本地',

  // Offline
  offlineMessage: '📶 网络已断开，部分功能不可用',
  backOnline: '📶 网络已恢复',

  // Error boundary
  errorBoundaryTitle: '页面出错了',
  errorBoundaryMessage: '发生了意外错误，请尝试刷新页面。',
  errorBoundaryRetry: '重试',

  // Tokens
  tokens: 'tokens',
} as const

export type I18nKey = keyof typeof zhCN
export default zhCN
