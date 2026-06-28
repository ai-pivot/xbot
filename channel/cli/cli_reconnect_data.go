package cli

// reconnectQuipsZH — 中文趣味重连语录。
// 每 ~4 秒通过 wall-clock tick 轮换一条。
var reconnectQuipsZH = []string{
	"正在顺着网线爬过去...",
	"别急，马上就好～",
	"正在施展重连魔法 ✨",
	"服务器：等等我！这就来～",
	"网络波动，请稍候喵～",
	"正在和服务器重新握手 🤝",
	"信号不好，换个姿势试试",
	"正在敲服务器的门 🚪",
	"请稍等，马上就连上啦～",
	"网络打了个盹，很快就醒",
	"正在建立量子纠缠...",
	"正在召唤服务器... 🪄",
	"网线有点松，拧紧中 🔧",
	"给服务器一个抱抱... 🤗",
	"正在用意念重连... 🧠",
	"数据包迷路了，正在找回",
	"蓄力中... 马上就连上了！",
	"正在呼叫 xbot 总部... 📡",
	"稍安勿躁，好事多磨～",
	"滴！正在重新登录中...",
	"服务器可能在喝咖啡 ☕",
	"网线被猫咪踩了一脚 🐱",
	"正在用摩尔斯电码重连...",
	"不要方，技术性调整而已",
	"正在 ping 1.1.1.1... 不通？再来！",
	"路由器：我还能抢救一下",
	"数据包正在翻山越岭...",
	"马上就连上了！大概... 也许...",
}

// reconnectQuipsEN — English fun reconnect quips.
var reconnectQuipsEN = []string{
	"Hold on, crawling through the cable...",
	"Almost there, pinky promise～",
	"Casting reconnect spell ✨",
	"Server: wait up! I'm coming～",
	"Network hiccup — be right back, nya～",
	"Re-handshaking with the server 🤝",
	"Bad signal, adjusting antenna...",
	"Knocking on the server's door 🚪",
	"Hang tight, reconnecting soon～",
	"The network took a nap, waking it up",
	"Establishing quantum entanglement...",
	"Summoning the server... 🪄",
	"Cable's a bit loose, tightening 🔧",
	"Giving the server a hug... 🤗",
	"Reconnecting via telepathy... 🧠",
	"Packets got lost, searching for them",
	"Charging up... almost there!",
	"Calling xbot headquarters... 📡",
	"Patience, young padawan～",
	"Beep! Logging in again...",
	"Server might be on a coffee break ☕",
	"A cat stepped on the network cable 🐱",
	"Reconnecting via carrier pigeon...",
	"Don't panic, it's just a technical adjustment",
	"Pinging 1.1.1.1... no? Retrying!",
	"Router: I can still be saved!",
	"Packets crossing mountains and rivers...",
	"Should be connected soon! Probably... maybe...",
}

// reconnectQuipsJA — 日本語の再接続中の楽しいメッセージ。
var reconnectQuipsJA = []string{
	"ケーブルの中を這ってます...",
	"もう少し、待っててね～",
	"再接続の魔法をかけてます ✨",
	"サーバー：ちょっと待って！今行く～",
	"ネットワークのしゃっくり、もうすぐ直るにゃ～",
	"サーバーと再握手してます 🤝",
	"電波が悪いので、姿勢を変えてます",
	"サーバーのドアをノックしてます 🚪",
	"しばらくお待ちください～",
	"ネットワークが居眠り中、起こします",
	"量子もつれを確立中...",
	"サーバーを召喚中... 🪄",
	"ケーブルが緩んでる、締め直し中 🔧",
	"サーバーにハグを... 🤗",
	"テレパシーで再接続中... 🧠",
	"パケットが迷子になりました、捜索中",
	"充電中... もうすぐ！",
	"xbot本部を呼び出し中... 📡",
	"慌てないで、すぐ繋がるから～",
	"ピッ！再ログイン中...",
	"サーバーはコーヒーブレイク中かも ☕",
	"猫がケーブルを踏んじゃった 🐱",
	"伝書鳩で再接続中...",
	"技術的な調整です、ご安心を",
	"1.1.1.1にping... ダメ？もう一回！",
	"ルーター：まだ復活できる！",
	"パケットが山を越えて川を渡って...",
	"もうすぐ繋がります！多分... きっと...",
}

// reconnectArts — small ASCII art scenes that cycle every ~8 seconds.
// Each art fits within 28 columns and 4 lines for narrow terminal safety.
var reconnectArts = [][]string{
	// 0: Cat — kawaii
	{
		"  ╱|、",
		" (˚ˎ 。7",
		"  |、˜〵",
		" じしˍ,)ノ",
	},
	// 1: Robot — tech buddy
	{
		"  [○_○]",
		"  ]|_|[",
		"  / > < \\",
	},
	// 2: Heart — sweet
	{
		"  .::\"\"::.",
		"  ::'  '::",
		"  :: ♡ ::",
		"  ':.  .:'",
		"    '::'",
	},
	// 3: Server rack — datacenter vibes
	{
		"  ╔══════╗",
		"  ║ ██ ██ ║",
		"  ║ ██ ██ ║",
		"  ╚══════╝",
	},
	// 4: Cable plug — connection theme
	{
		"   ╭─────╮",
		" ──┤·····├──",
		"   ╰─────╯",
	},
	// 5: Shooting star — wish
	{
		"  ☆",
		"   ╲",
		"    ╲  ✧",
		"     ╲",
		"      ★",
	},
}

// selectReconnectQuip picks a quip by language and tick.
// lang is the locale language code ("zh", "en", "ja").
// tick is wall-clock 100ms ticks (time.Now().UnixMilli()/100).
func selectReconnectQuip(lang string, tick int64) string {
	var quips []string
	switch lang {
	case "ja":
		quips = reconnectQuipsJA
	case "en":
		quips = reconnectQuipsEN
	default:
		quips = reconnectQuipsZH
	}
	if len(quips) == 0 {
		return ""
	}
	// Cycle every 4 seconds (40 ticks)
	return quips[(tick/40)%int64(len(quips))]
}

// selectReconnectArt picks an ASCII art scene by tick.
// Cycles every 8 seconds (80 ticks).
func selectReconnectArt(tick int64) []string {
	if len(reconnectArts) == 0 {
		return nil
	}
	return reconnectArts[(tick/80)%int64(len(reconnectArts))]
}
