package cli

// =============================================================================
// Reconnect Splash Data — quips & ASCII art with hidden easter eggs
// =============================================================================
//
// Rarity system (deterministic per wall-clock art-cycle, no stored state):
//
//   reconnectRarity(tick) returns:
//     0 = normal  (~94% of art cycles)  — 6 standard arts + full quip pool
//     1 = rare    (~5%  of art cycles)  — 3 rare arts + rare quips mixed in
//     2 = golden  (~1%  of art cycles)  — 1 golden art + golden quips
//
// Rarity is deterministic per 8-second art-cycle window (tick/80), not per
// 100ms frame — so the rare/golden content stays visible for a full cycle
// rather than flickering.

// =============================================================================
// Normal quip sets (i18n)
// =============================================================================

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

// =============================================================================
// Normal ASCII art scenes (6 total, cycle every ~8 seconds)
// All characters are halfwidth (ASCII + box-drawing + solid block) to avoid
// alignment drift from CJK/fullwidth character mixing.
// =============================================================================

var reconnectArts = [][]string{
	// 0: Cat — kawaii (7 cols wide)
	{
		" |\\__/|",
		" (o  o)",
		"  >^^<",
	},
	// 1: Diamond — shiny (7 cols wide)
	{
		"   /\\",
		"  /  \\",
		" /    \\",
		" \\    /",
		"  \\  /",
		"   \\/",
	},
	// 2: Star — simple (6 cols wide)
	{
		"   *",
		"  ***",
		" *****",
		"  ***",
		"   *",
	},
	// 3: Server — rack (10 cols wide)
	{
		"+--------+",
		"| [==]   |",
		"| [==]   |",
		"+--------+",
	},
	// 4: Connection — cable (9 cols wide)
	{
		"  o    o",
		"---------",
		"  \\    /",
		"   \\  /",
		"    \\/",
	},
	// 5: Ghost — spooky cute (6 cols wide)
	{
		"  .-.",
		" (o o)",
		" | O |",
		"  '-'",
	},
}

// =============================================================================
// Rare easter egg (~5% of art cycles)
// =============================================================================

var reconnectArtsRare = [][]string{
	// 0: Rocket — launch (8 cols wide)
	{
		"   /\\",
		"  /  \\",
		" /    \\",
		" | [] |",
		" | [] |",
		" /----\\",
	},
	// 1: Diamond — repeat of normal for consistency
	{
		"   /\\",
		"  /  \\",
		" /    \\",
		" \\    /",
		"  \\  /",
		"   \\/",
	},
	// 2: Fish — swimming (7 cols wide)
	{
		" ><>",
		"<><><>",
		" ><>",
	},
}

var reconnectQuipsRareZH = []string{
	"🐛 发现一只稀有 bug，正在捕获...",
	"⚡ 运气不错！今天是重连日～",
	"🎰 叮叮叮！稀有重连动画解锁！",
	"🔮 水晶球显示：马上就连上了",
	"🦄 独角兽正在帮你重连...",
	"🎪 马戏团数据包正在赶来的路上",
}

var reconnectQuipsRareEN = []string{
	"🐛 A wild rare bug appeared! Catching it...",
	"⚡ Lucky day! Rare reconnect sequence!",
	"🎰 Jackpot! Rare reconnect animation unlocked!",
	"🔮 The crystal ball says: reconnecting soon!",
	"🦄 A unicorn is assisting your reconnect...",
	"🎪 Circus packets en route to your terminal!",
}

var reconnectQuipsRareJA = []string{
	"🐛 レアバグ発見！捕獲中...",
	"⚡ ラッキー！レア再接続デー！",
	"🎰 ジャックポット！レアアニメーション解放！",
	"🔮 水晶玉が示す：もうすぐ繋がる！",
	"🦄 ユニコーンが再接続をお手伝い...",
	"🎪 サーカスパケットが到着中！",
}

// =============================================================================
// Golden easter egg (~1% of art cycles — ultra rare)
// =============================================================================

var reconnectArtsGolden = [][]string{
	// 0: Crown — you found it! (9 cols wide)
	{
		"  .-.-.",
		" /  ^  \\",
		"|  \\_/  |",
		" \\_____/",
		"  |   |",
		"  |   |",
	},
}

var reconnectQuipsGoldenZH = []string{
	"👑 天选之人！传说级重连动画解锁！！",
}

var reconnectQuipsGoldenEN = []string{
	"👑 The Chosen One! Legendary reconnect unlocked!!",
}

var reconnectQuipsGoldenJA = []string{
	"👑 選ばれし者！伝説の再接続アニメーション解放！！",
}

// =============================================================================
// Helper functions
// =============================================================================

// reconnectRarity returns the rarity tier for the given tick.
//
//	0 = normal      (~97.6%)  — standard art + standard quips
//	1 = rare quips  (~2%)     — standard art + rare quips mixed into pool
//	2 = rare arts   (~0.4%)   — rare art + rare quips
//	3 = hidden      (special) — golden art + golden quip
//
// Tiers 1-2 are deterministic per art-cycle (tick/80). Tier 3 uses a
// hidden trigger: the art-cycle number must equal a specific magic value
// that occurs once every ~8 hours of continuous disconnection — if you
// happen to be disconnected at that exact 8-second window, you win.
func reconnectRarity(tick int64) int {
	cycle := tick / 80 // 8-second art cycle

	// Hidden: ~0.027% — once per 3600 cycles (≈8 hours)
	// Specific cycle number 1337 acts as the "secret code"
	if cycle%3600 == 1337 {
		return 3
	}
	// Rare arts: ~0.4% — once per ~34 minutes (257 is prime)
	if cycle%257 == 127 {
		return 2
	}
	// Rare quips: ~2% — once per ~7 minutes (53 is prime)
	if cycle%53 == 23 {
		return 1
	}
	return 0
}

// selectReconnectQuip picks a quip by language, tick, and rarity.
func selectReconnectQuip(lang string, tick int64) string {
	rarity := reconnectRarity(tick)

	var quips []string
	switch rarity {
	case 3: // hidden golden
		switch lang {
		case "ja":
			quips = reconnectQuipsGoldenJA
		case "en":
			quips = reconnectQuipsGoldenEN
		default:
			quips = reconnectQuipsGoldenZH
		}
	case 2, 1: // rare arts or rare quips — mix rare quips into pool
		var rare []string
		switch lang {
		case "ja":
			rare = reconnectQuipsRareJA
		case "en":
			rare = reconnectQuipsRareEN
		default:
			rare = reconnectQuipsRareZH
		}
		quips = append(append([]string{}, reconnectQuips(lang)...), rare...)
	default: // normal
		quips = reconnectQuips(lang)
	}

	if len(quips) == 0 {
		return ""
	}
	return quips[(tick/40)%int64(len(quips))]
}

// selectReconnectArt picks an ASCII art scene by tick and rarity.
func selectReconnectArt(tick int64) []string {
	rarity := reconnectRarity(tick)

	var arts [][]string
	switch rarity {
	case 3:
		arts = reconnectArtsGolden
	case 2:
		arts = reconnectArtsRare
	default:
		arts = reconnectArts
	}

	if len(arts) == 0 {
		return nil
	}
	return arts[(tick/80)%int64(len(arts))]
}

// reconnectQuips returns the normal quip set for a language.
func reconnectQuips(lang string) []string {
	switch lang {
	case "ja":
		return reconnectQuipsJA
	case "en":
		return reconnectQuipsEN
	default:
		return reconnectQuipsZH
	}
}
