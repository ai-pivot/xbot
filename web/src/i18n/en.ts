/**
 * i18n Constants — en
 * English translations for the Web UI.
 * Must have identical keys to zh-CN.ts (enforced by type).
 */
import type { I18nKey } from './zh-CN'

const en: Record<I18nKey, string> = {
  // ─── App ───
  appName: 'xbot',
  appSubtitle: 'AI Chat Assistant',
  loading: 'Loading...',
  logoutBtn: 'Logout',
  skipToContent: 'Skip to content',

  // ─── Auth ───
  login: 'Login',
  register: 'Register',
  logout: 'Logout',
  username: 'Username',
  password: 'Password',
  loginFailed: 'Login failed',
  registerFailed: 'Registration succeeded but auto-login failed. Please login manually.',
  networkError: 'Network error',
  operationFailed: 'Operation failed',
  enterUsername: 'Enter username',
  enterPassword: 'Enter password',
  noAccount: "Don't have an account? Register",
  hasAccount: 'Already have an account? Login',
  createAccount: 'Create Account',
  feishuLogin: 'Feishu Login',
  feishuAccountLogin: 'Feishu Account Login',
  feishuLoginFailed: 'Feishu login failed',
  feishuUserId: 'Feishu User ID',
  feishuPassword: 'Linked Web account password',
  feishuPasswordLabel: 'Web Password',
  feishuBindHint: 'You need to bind a Web account in Feishu first, then use the password set during binding.',
  feishuPlaceholder: 'ou_xxx or open_id',
  backToPasswordLogin: '← Back to password login',
  loginViaFeishu: 'Login via Feishu User ID →',

  // ─── Chat ───
  startConversation: 'Start a conversation',
  sendFirstMessage: 'Send a message to start chatting with the AI assistant',
  searchHistory: 'Search history',
  viewCommands: 'View commands',
  inputPlaceholder: 'Type a message... (Shift+Enter for new line)',
  connecting: 'Connecting...',
  send: 'Send',
  sendMessage: 'Send message',
  stopGeneration: 'Stop generation',
  scrollToBottom: 'Scroll to bottom',
  newMessages: 'New messages',
  conversationCleared: 'Conversation cleared',
  newConversation: 'New conversation',
  copyContent: 'Copy content',
  copied: '✓ Copied',
  copyCode: 'Copy code',
  searchKbHint: 'Search history',
  commandHint: 'View commands',
  switchedToModel: 'Switched to {model}',
  openSettings: 'Open settings',
  messagesAriaLabel: 'Messages',
  imagePreview: 'Image preview',
  closePreview: 'Close preview',
  imagePreviewAlt: 'Preview',
  cancelGeneration: 'Cancel generation',
  helpTitle: 'Available commands',

  // ─── Status ───
  connected: '● Connected',
  connectingStatus: '◐ Connecting...',
  disconnected: '○ Disconnected',
  serverDisconnected: '⛔ Server disconnected. Please refresh the page to reconnect.',
  reconnecting: '⚠️ Connection lost, reconnecting...',
  contextUsage: 'Context usage',
  contextUsageTitle: 'Context usage: {prompt} / {max} tokens',

  // ─── Settings ───
  settings: '⚙️ Settings',
  closeSettings: 'Close settings',
  saving: 'Saving...',
  appearance: 'Appearance',
  sessions: 'Sessions',
  presets: 'Presets',
  llm: 'LLM',
  runner: 'Runner',
  market: 'Market',
  theme: 'Theme',
  dark: 'Dark',
  light: 'Light',
  fontSize: 'Font size',
  nickname: 'Nickname',
  switchModel: 'Switch model',
  modelSelect: 'Model selection',
  settingsSaved: 'Settings saved',
  saveFailed: 'Save failed. Please try again.',

  // ─── Search ───
  searchMessages: 'Search message history...',
  searching: 'Searching...',
  noResults: 'No results found',

  // ─── File upload ───
  uploadFile: 'Upload file',
  uploadFailed: 'Upload failed',
  fileTooLarge: 'File exceeds 10MB limit',
  uploadResponseError: 'Upload response error',
  dragToUpload: 'Drag files here to upload',
  dragSupportedTypes: 'Supports images, documents, code and other files',
  remove: 'Remove',

  // ─── Commands ───
  cmdClear: 'Clear conversation',
  cmdNew: 'New conversation',
  cmdCompact: 'Compress context',
  cmdHelp: 'Show help',
  cmdCancel: 'Cancel generation',

  // ─── AskUser ───
  agentNeedsInput: 'Agent needs your input',
  inputAnswer: 'Type your answer...',
  submit: 'Submit',
  previousQuestion: '← Previous',
  cancel: 'Cancel',

  // ─── Progress ───
  thinking: 'Thinking...',
  rendering: 'Rendering chart...',
  executingTool: 'Executing tool...',
  processing: 'Processing...',
  preparing: 'Preparing...',

  // ─── Mermaid ───
  mermaidRenderFailed: 'Mermaid render failed',

  // ─── Image / File ───
  image: 'Image',
  file: 'File',

  // ─── Confirm dialog ───
  confirm: 'Confirm',

  // ─── Sidebar ───
  searchPlaceholder: 'Search sessions...',
  expandSidebar: 'Expand session list',
  chatSessions: '💬 Sessions',
  newSession: 'New session',
  collapseSidebar: 'Collapse',
  unnamedSession: 'Unnamed',
  currentSession: 'Current',
  refreshSessions: '🔄 Refresh',
  noSessions: 'No sessions',
  deleteSession: 'Delete session',
  renameSession: 'Rename',
  confirmDeleteSession: 'Are you sure you want to delete this session? This action cannot be undone.',
  sidebarLoading: 'Loading...',

  // ─── Errors ───
  switchFailed: 'Switch failed',

  // ─── Runner ───
  local: 'Local',
  workspaceEnv: '🖥️ Workspace',
  manageRunners: 'Manage remote Runners. Click a card to switch the active environment.',
  noRunners: 'No workspaces added',
  addRunnerHint: 'Add a Runner to execute commands remotely',
  activeBadge: 'Active',
  dockerSandbox: '🐳 Docker Sandbox (Built-in)',
  builtinEnv: 'Built-in environment',
  copyConnectCommand: 'Copy connect command',
  copiedCommand: 'Copied!',
  deleteRunner: 'Delete',
  confirmDeleteRunner: 'Confirm delete',
  confirmDeleteRunnerText: 'Are you sure you want to delete {name}?',
  runnerOnlineWarning: '⚠️ This Runner is currently online. Deleting will disconnect it.',
  runnerName: 'Name',
  runnerNamePlaceholder: 'e.g. MacBook Pro',
  runMode: 'Run mode',
  nativeMode: '🖥️ Native',
  dockerMode: '🐳 Docker',
  dockerImage: 'Docker Image',
  workspace: 'Workspace',
  workspacePlaceholder: 'e.g. /home/user/project (leave empty for auto-detect)',
  workspaceHint: 'Runner will use this directory as workspace after connecting',
  creating: '⏳ Creating...',
  create: '✨ Create',
  addWorkspace: '➕ Add workspace',

  // ─── Offline ───
  offlineMessage: '📶 Network disconnected. Some features are unavailable.',
  backOnline: '📶 Network restored',

  // ─── Error boundary ───
  errorBoundaryTitle: 'Something went wrong',
  errorBoundaryMessage: 'An unexpected error occurred. Please try refreshing the page.',
  errorBoundaryRetry: 'Retry',
  refreshPage: 'Refresh page',
  errorDetails: 'Error details',

  // ─── Tokens ───
  tokens: 'tokens',

  // ─── Appearance Tab ───
  appearanceTitle: '🎨 Appearance',
  themeLabel: 'Theme',
  fontSizeLabel: 'Font Size',
  nicknameLabel: 'Nickname',
  languageLabel: 'Language',
  smallSize: 'Small',
  mediumSize: 'Medium',
  largeSize: 'Large',
  enterNickname: 'Enter nickname...',

  // ─── Sessions Tab ───
  chatRooms: '💬 ChatRooms',
  chatRoomDesc: 'All conversations are ChatRooms — Person↔Agent, Agent↔Agent unified management.',
  backToList: '← Back to list',
  mainSession: '👤 Main',
  agentSession: '🤖 Agent',
  running: 'Running',
  completed: 'Completed',
  noMessages: 'No messages',
  noChatRooms: 'No ChatRooms',
  loadingDots: 'Loading...',
  refresh: '🔄 Refresh',

  // ─── LLM Tab ───
  llmTitle: '🧠 Personal LLM Config',
  fetchConfigFailed: 'Failed to load config',
  providerLabel: 'Provider',
  currentModelLabel: 'Current model',
  usingPersonalModel: 'Using personal model. You can switch models or delete the config to restore system default.',
  addLLMConfig: 'Add personal LLM config',
  baseURLRequired: 'Base URL and API Key are required',
  configSaved: 'Config saved',
  modelSwitched: 'Model switched',
  switchModelFailed: 'Failed to switch model',
  configDeleted: 'Config deleted',
  deleteFailed: 'Delete failed',
  deleteLLMConfig: 'Delete personal LLM config? This will restore the system default model.',
  deleteConfig: 'Delete config',
  addConfig: 'Add config',
  saveConfig: 'Save config',
  save: 'Save',
  defaultModel: 'Default',
  apiKeyMasked: '••••••••',
  maxContextLabel: 'Max Context Tokens',
  maxOutputLabel: 'Max Output Tokens',
  thinkingModeLabel: 'Thinking Mode',
  thinkingAuto: 'Auto',
  thinkingOn: 'On',
  thinkingOff: 'Off',
  baseURLLabel: 'Base URL',
  apiKeyLabel: 'API Key',
  modelLabel: 'Model',
  enterModelName: 'Enter model name...',

  // ─── Presets Tab ───
  presetsTitle: '⚡ Presets',
  presetsDesc: 'Configure quick commands for fast access in the chat input. Max 20 entries.',
  presetIcon: 'Icon',
  presetLabel: 'Label',
  presetContent: 'Content',
  presetContentPlaceholder: 'Content to send on click...',
  fillMode: 'Fill mode (fill into editor instead of direct send)',
  savePreset: '💾 Save',
  confirmDeletePreset: 'Delete this preset?',
  noPresets: 'No presets',
  moveUp: 'Move up',
  moveDown: 'Move down',
  editPreset: 'Edit',
  deletePreset: 'Delete',

  // ─── Market Tab ───
  marketTitle: '🏪 Market',
  publishSuccess: 'Published',
  unpublishSuccess: 'Unpublished',
  installSuccess: 'Installed',
  uninstallSuccess: 'Uninstalled',
  installFailed: 'Install failed',
  uninstallFailed: 'Uninstall failed',

  // ─── AssistantTurn ───
  thinkingProcess: 'Thinking process',
  iterationProcess: 'Iteration process',
  expandAll: 'Expand all',
  collapse: 'Collapse',

  // ─── Tab labels ───
  tabAppearance: 'Appearance',
  tabSessions: 'Sessions',
  tabPresets: 'Presets',
  tabLLM: 'LLM',
  tabRunner: 'Runner',
  tabMarket: 'Market',

  // ─── Misc ───
  refreshPageHint: 'Please refresh the page to reconnect',
  delete: 'Delete',
  returnToLogin: 'Return to login',
}

export default en
