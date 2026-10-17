export const previewCopy = {
  en: {
    authorize: 'Open backend authorization', token: 'Short-lived preview token', enter: 'Verify & connect', consent: 'I trust this PR and authorize access to my real files, tools and full workspace. This is not read-only.', verified: 'Preview authorization expired. Authorize again to continue.',
    eyebrow: 'FRONTEND PREVIEW', title: 'Choose your backend',
    intro: 'This deployment contains the interface only. Connect to an xbot backend you control.',
    label: 'Backend address', hint: 'HTTP(S) only. A subpath is kept as the backend prefix. No credentials, query or fragment.',
    save: 'Save address', test: 'Test connection', testing: 'Testing…', cancel: 'Cancel test',
    reset: 'Reset & reload', change: 'Change backend',
    warningTitle: 'Your workspace, your responsibility',
    warning: 'Once authorized, this PR’s code can access the backend’s real files, tools and workspace. Use a trusted preview and an isolated backend. Never enter model keys here.',
    blockedTitle: 'Explicit authorization required',
    blocked: 'Cross-site cookie authentication does not work with the current backend. A successful test only checks reachability; it does not sign you in or open the app. Use the explicit, short-lived preview authorization below to connect.',
    idle: 'No connection test yet. No model requests will be made.',
    success: 'Public backend endpoint reached. Authentication remains unverified; the app is still locked.',
    invalid: 'Enter a full http:// or https:// address without user info, query or fragment.',
    saved: 'Address saved in this browser only.', storage: 'Browser storage is unavailable. This address could not be saved.',
    timeout: 'Connection timed out. Check the backend and browser permissions, then retry.',
    network: 'Could not reach the backend. This may be CORS, browser local-network permission, mixed content, TLS or an offline server; the browser cannot distinguish them reliably.',
    response: 'The server did not return the expected xbot public auth configuration. Check the backend address and subpath.',
    http: 'The backend returned HTTP', cancelled: 'Connection test cancelled.',
  },
  'zh-CN': {
    authorize: '打开后端授权页面', token: '短期预览令牌', enter: '验证并连接', consent: '我信任此 PR，并授权其访问真实文件、工具和完整工作区。这不是只读访问。', verified: '预览授权已过期，请重新授权。',
    eyebrow: '前端预览', title: '选择你的后端', intro: '此部署只包含前端界面。请连接到你控制的 xbot 后端。',
    label: '后端地址', hint: '仅支持 HTTP(S)。子路径将保留为后端前缀；不允许用户凭据、查询参数或片段。',
    save: '保存地址', test: '测试连接', testing: '正在测试…', cancel: '取消测试', reset: '重置并刷新', change: '更换后端',
    warningTitle: '请谨慎授权真实工作区', warning: '授权后，此 PR 的代码可访问后端的真实文件、工具和工作区。请使用可信预览与隔离后端，不要在此输入模型密钥。',
    blockedTitle: '需要显式预览授权', blocked: '当前后端的跨站 Cookie 认证不可用。测试成功只表示端点可达，不代表已登录，也不会打开应用；请通过下方的短期预览令牌显式授权后连接。',
    idle: '尚未测试连接。不会发起模型请求。', success: '公共后端端点可达。认证仍未验证，应用保持锁定。',
    invalid: '请输入完整的 http:// 或 https:// 地址，不含用户凭据、查询参数或片段。', saved: '地址已保存在当前浏览器中。', storage: '浏览器存储不可用，无法保存此地址。',
    timeout: '连接超时。请检查后端与浏览器权限后重试。', network: '无法连接后端。可能是 CORS、本地网络权限、混合内容、TLS 或服务离线；浏览器无法可靠区分这些原因。',
    response: '服务器未返回预期的 xbot 公共认证配置，请检查后端地址与子路径。', http: '后端返回 HTTP', cancelled: '已取消连接测试。',
  },
  ja: {
    authorize: 'バックエンドの承認画面を開く', token: '短期プレビュートークン', enter: '検証して接続', consent: 'この PR を信頼し、実際のファイル・ツール・ワークスペース全体へのアクセスを許可します。読み取り専用ではありません。', verified: 'プレビュー認証が期限切れです。再度承認してください。',
    eyebrow: 'フロントエンドプレビュー', title: 'バックエンドを選択', intro: 'このデプロイは画面のみです。管理下の xbot バックエンドを指定してください。',
    label: 'バックエンドアドレス', hint: 'HTTP(S) のみ。サブパスはバックエンドのプレフィックスになります。認証情報・クエリ・フラグメントは使用できません。',
    save: 'アドレスを保存', test: '接続テスト', testing: 'テスト中…', cancel: 'テストを中止', reset: 'リセットして再読み込み', change: 'バックエンドを変更',
    warningTitle: '実際のワークスペースに注意', warning: '承認後、この PR のコードは実際のファイル・ツール・ワークスペースにアクセスできます。信頼できるプレビューと隔離したバックエンドを使用し、モデルキーは入力しないでください。',
    blockedTitle: '明示的な承認が必要です', blocked: '現在のバックエンドではクロスサイト Cookie 認証は動作しません。テスト成功は到達可能性のみを示し、ログインやアプリの起動は行いません。接続には下記の短期プレビュー認証が必要です。',
    idle: '接続は未テストです。モデルへのリクエストは行いません。', success: '公開エンドポイントに到達しました。認証は未検証のため、アプリはロックされたままです。',
    invalid: '認証情報・クエリ・フラグメントを含まない http:// または https:// の完全なアドレスを入力してください。', saved: 'このブラウザーにアドレスを保存しました。', storage: 'ブラウザーのストレージが使用できないため、保存できませんでした。',
    timeout: '接続がタイムアウトしました。バックエンドとブラウザーの権限を確認してください。', network: '接続できません。CORS・ローカルネットワーク権限・混在コンテンツ・TLS・サーバー停止などが考えられ、ブラウザーでは確実に区別できません。',
    response: 'xbot の公開認証設定が返されませんでした。アドレスとサブパスを確認してください。', http: 'バックエンドの HTTP ステータス:', cancelled: '接続テストを中止しました。',
  },
} as const

export function previewLocale(language: string): keyof typeof previewCopy {
  if (language.toLowerCase().startsWith('en')) return 'en'
  if (language.toLowerCase().startsWith('ja')) return 'ja'
  return 'zh-CN'
}
