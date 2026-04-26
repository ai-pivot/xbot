package channel

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Easter egg state constants
// ---------------------------------------------------------------------------

// easterEggMode Represents the currently active easter egg type
type easterEggMode string

const (
	easterEggNone     easterEggMode = ""
	easterEggKonami   easterEggMode = "konami"
	easterEggMatrix   easterEggMode = "matrix"
	easterEggAnswer42 easterEggMode = "answer42"
	easterEggVersion  easterEggMode = "version"
)

// ---------------------------------------------------------------------------
// Easter egg internal message types
// ---------------------------------------------------------------------------

// easterEggDoneMsg Easter egg dismiss message (triggered by any key press)
type easterEggDoneMsg struct{}

// easterEggMatrixTickMsg Matrix rain animation tick
type easterEggMatrixTickMsg struct{}

// ---------------------------------------------------------------------------
// Konami Code (↑↑↓↓←→←→BA)
// ---------------------------------------------------------------------------

var konamiSequence = []string{"up", "up", "down", "down", "left", "right", "left", "right", "b", "a"}

var konamiASCII = strings.TrimLeft(`
     KONAMI CODE ACTIVATED!
     ======================

         ↑ ↑ ↓ ↓ ← → ← → B A

     +30 Lives
     (Well, not really, but you found the secret!)

          * * *   GAME OVER? NO!   * * *

     [ Press any key to dismiss ]
`, "\n")

// checkKonami Check if keypress matches Konami Code sequence
func (m *cliModel) checkKonami(keyName string) bool {
	if m.konamiBuffer == nil {
		m.konamiBuffer = make([]string, 0, len(konamiSequence))
	}
	m.konamiBuffer = append(m.konamiBuffer, keyName)

	if len(m.konamiBuffer) > len(konamiSequence) {
		m.konamiBuffer = m.konamiBuffer[len(m.konamiBuffer)-len(konamiSequence):]
	}

	if len(m.konamiBuffer) >= len(konamiSequence) {
		offset := len(m.konamiBuffer) - len(konamiSequence)
		match := true
		for i := 0; i < len(konamiSequence); i++ {
			if m.konamiBuffer[offset+i] != konamiSequence[i] {
				match = false
				break
			}
		}
		if match {
			m.konamiBuffer = nil
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 彩蛋 #2: /matrix — Matrix rain
// ---------------------------------------------------------------------------

var matrixChars = []rune{
	'ｱ', 'ｲ', 'ｳ', 'ｴ', 'ｵ', 'ｶ', 'ｷ', 'ｸ', 'ｹ', 'ｺ',
	'ｻ', 'ｼ', 'ｽ', 'ｾ', 'ｿ', 'ﾀ', 'ﾁ', 'ﾂ', 'ﾃ', 'ﾄ',
	'ﾅ', 'ﾆ', 'ﾇ', 'ﾈ', 'ﾉ', 'ﾊ', 'ﾋ', 'ﾌ', 'ﾍ', 'ﾎ',
	'ﾏ', 'ﾐ', 'ﾑ', 'ﾒ', 'ﾓ', 'ﾔ', 'ﾕ', 'ﾖ', 'ﾗ', 'ﾘ',
	'ﾙ', 'ﾚ', 'ﾛ', 'ﾜ', 'ﾝ', '0', '1', '2', '3', '4',
	'5', '6', '7', '8', '9', ':', '.', '*', '+', '-', '=',
}

// initMatrixColumns Initialize rain column states
func (m *cliModel) initMatrixColumns() {
	cols := m.width
	if cols < 10 {
		cols = 10
	}
	rows := m.height
	if rows < 5 {
		rows = 5
	}
	m.matrixCols = cols
	m.matrixRows = rows
	m.matrixDrops = make([]int, cols)
	m.matrixSpeeds = make([]int, cols)
	m.matrixTrailLen = make([]int, cols)
	for i := 0; i < cols; i++ {
		m.matrixDrops[i] = -rand.Intn(rows) // Negative = still off screen
		m.matrixSpeeds[i] = 1 + rand.Intn(2)
		m.matrixTrailLen[i] = 5 + rand.Intn(15)
	}
	// Initialize matrix buffer with spaces
	m.matrixBuffer = make([][]rune, rows)
	for r := 0; r < rows; r++ {
		m.matrixBuffer[r] = make([]rune, cols)
		for c := 0; c < cols; c++ {
			m.matrixBuffer[r][c] = ' '
		}
	}
}

// tickMatrix Advance one frame of rain animation
func (m *cliModel) tickMatrix() {
	if m.matrixDrops == nil {
		m.initMatrixColumns()
	}

	cols := m.matrixCols
	rows := m.matrixRows
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Randomly update existing characters for flicker effect
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if m.matrixBuffer[r][c] != ' ' && rng.Intn(10) == 0 {
				m.matrixBuffer[r][c] = matrixChars[rng.Intn(len(matrixChars))]
			}
		}
	}

	// Advance each column's drop
	for c := 0; c < cols; c++ {
		m.matrixDrops[c] += m.matrixSpeeds[c]
		head := m.matrixDrops[c]
		tail := head - m.matrixTrailLen[c]

		// Write new character at head
		if head >= 0 && head < rows {
			m.matrixBuffer[head][c] = matrixChars[rng.Intn(len(matrixChars))]
		}
		// Erase at tail
		if tail >= 0 && tail < rows {
			m.matrixBuffer[tail][c] = ' '
		}
		// Off screen: reset
		if tail > rows+5 {
			m.matrixDrops[c] = -rng.Intn(rows / 2)
			m.matrixSpeeds[c] = 1 + rng.Intn(2)
			m.matrixTrailLen[c] = 5 + rng.Intn(15)
		}
	}
}

// matrixTickCmd Generate tick command for next Matrix animation frame (~12fps)
func matrixTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return easterEggMatrixTickMsg{}
	})
}

// ---------------------------------------------------------------------------
// 彩蛋 #3: The Answer is 42
// ---------------------------------------------------------------------------

var answer42Art = strings.TrimLeft(`
    D E E P   T H O U G H T
    ========================

    The Answer to the Ultimate Question
    of Life, the Universe, and Everything...

              42

    "Though I don't think," added Deep Thought,
    "that you're going to like it."

    [ Press any key to dismiss ]
`, "\n")

// isAnswer42 Detect if user input triggers "The Answer is 42" easter egg
func isAnswer42(content string) bool {
	lower := strings.ToLower(content)
	patterns := []string{
		"the answer to life",
		"the answer to the ultimate question",
		"ultimate question of life",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 彩蛋 #4: Holiday Splash description
// ---------------------------------------------------------------------------

func holidaySplash() string {
	now := time.Now()
	month, day := int(now.Month()), now.Day()

	if month == 1 && day == 1 {
		return "Happy New Year " + fmt.Sprintf("%d!", now.Year())
	}
	if month == 2 && day == 14 {
		return "Happy Valentine's Day - May your code compile on the first try"
	}
	if month == 3 && day == 14 {
		return "3.14159265358979... Happy Pi Day!"
	}
	if month == 4 && day == 1 {
		return "All bugs are features today - Happy April Fools'!"
	}
	if month == 9 {
		isLeap := isLeapYear(now.Year())
		pDay := 12
		if isLeap {
			pDay = 13
		}
		if day == pDay {
			return "Happy Programmers' Day (2^8 = 256)"
		}
	}
	if month == 10 && day == 31 {
		return "Boo! Runtime errors lurk in every commit..."
	}
	if month == 12 && day == 25 {
		return "Merry Christmas! May all PRs merge smoothly"
	}
	return ""
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// ---------------------------------------------------------------------------
// 彩蛋 #5: /sudo — Permission denied
// ---------------------------------------------------------------------------

var sudoMessages = []string{
	"root is not in the sudoers file. This incident will be reported to /dev/null.",
	"Nice try. Permission denied. Try /help instead.",
	"I'm sorry Dave, I'm afraid I can't do that.",
	"ACCESS DENIED. Please contact your system administrator (you).",
	"You shall not pass! -- Gandalf",
	"Segmentation fault (core dumped). Just kidding.",
	"Error: Insufficient karma. Try contributing to open source first.",
	"403 Forbidden: Even the Matrix can't grant you sudo access here.",
	"sudo: a terminal error has occurred. Try rebooting the universe.",
	"Warning: Running with sudo may cause spontaneous combustion.",
}

func randomSudoMessage() string {
	return sudoMessages[rand.Intn(len(sudoMessages))]
}

// ---------------------------------------------------------------------------
// 彩蛋 #6: /fortune — Programmer fortune cookie
// ---------------------------------------------------------------------------

var fortuneMessages = []struct {
	text  string
	lucky int
}{
	{"A well-written test is worth a thousand bug reports.", 7},
	{"Your code will compile on the first try today. Probably.", 42},
	{"Great debugging session awaits you. Coffee helps.", 13},
	{"Trust your types. Let the compiler be your guide.", 21},
	{"A chance encounter with a semicolon will change your life.", 88},
	{"The bug you seek is not where you think it is.", 64},
	{"Someone will refactor your legacy code. Rejoice.", 3},
	{"An unexpected git bisect will reveal the truth.", 27},
	{"Your pull request will be approved without comments.", 99},
	{"The answer lies in the logs. Always check the logs.", 1},
	{"Today is a good day to delete dead code.", 55},
	{"A clever one-liner will impress your reviewer.", 16},
	{"Embrace the merge conflict. Growth comes from resolution.", 33},
	{"The stack trace is long, but the fix is one line.", 73},
	{"Do not fear the legacy code. It was once modern too.", 48},
	{"Your CI pipeline will be green today. All tests pass.", 100},
	{"A rubber duck will reveal what hours of debugging could not.", 9},
	{"The best code is no code. The second best is someone else's.", 0},
	{"A dependency update will break everything. Pin your versions.", 66},
	{"Your log messages will be poetic and informative.", 37},
}

func randomFortune() (string, int) {
	f := fortuneMessages[rand.Intn(len(fortuneMessages))]
	return f.text, f.lucky
}

// ---------------------------------------------------------------------------
// 彩蛋 #7: Triple /version — version OCD achievement
// ---------------------------------------------------------------------------

var versionAchievementArt = strings.TrimLeft(`
    ACHIEVEMENT UNLOCKED!
    =====================

      " Version OCD "

    You checked the version 3 times
    in under 10 seconds.

    Yes, it's still %s

         +100 OCD points

    [ Press any key to dismiss ]
`, "\n")

// recordVersionHit Record /version call, return true if easter egg was triggered
func (m *cliModel) recordVersionHit() bool {
	now := time.Now()
	m.versionHitTimes = append(m.versionHitTimes, now)
	if len(m.versionHitTimes) > 3 {
		m.versionHitTimes = m.versionHitTimes[len(m.versionHitTimes)-3:]
	}
	if len(m.versionHitTimes) >= 3 {
		elapsed := m.versionHitTimes[len(m.versionHitTimes)-1].Sub(m.versionHitTimes[len(m.versionHitTimes)-3])
		if elapsed <= 10*time.Second {
			m.versionHitTimes = nil
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 彩蛋 #8: /zen — Zen moment
// ---------------------------------------------------------------------------

var zenHaiku = []struct {
	haiku   string
	message string
}{
	{"Code flows like water,\nBugs hide in the dark,\nTests light the way.", "The best error message is the one that never shows up."},
	{"Keys click like rain,\nScreen glows at midnight,\nCoffee grows cold.", "Before debugging, take a walk. The answer comes when you stop looking."},
	{"Features pile like mountains,\nSimplicity is hardest,\nLess is more.", "Perfection is achieved when there is nothing left to take away."},
	{"Functions short as poems,\nNames clear as day,\nRefactor daily.", "Code is like humor. When you have to explain it, it's bad."},
	{"Git commits clean,\nHistory easy to trace,\nFuture me thanks.", "Commit early, commit often. Your future self will thank you."},
	{"Terminal dark as night,\nCursor blinks like stars,\nCode is the cosmos.", "In the beginning there was nothing. Then someone wrote git init."},
	{"Zero warnings,\nAll tests green,\nDeploy in a heartbeat.", "The feeling of all tests passing is the programmer's greatest high."},
	{"Spaces or tabs?\nA thousand years of debate,\nUse prettier.", "The strongest warriors are these two -- time and patience."},
}

func randomZen() (string, string) {
	z := zenHaiku[rand.Intn(len(zenHaiku))]
	return z.haiku, z.message
}

// ---------------------------------------------------------------------------
// Easter egg activation/rendering — centralized management
// ---------------------------------------------------------------------------

// activateEasterEgg Activate specified easter egg (dismiss on any key press).
// Return tea.Cmd for Matrix animation's initial tick.
func (m *cliModel) activateEasterEgg(mode easterEggMode) tea.Cmd {
	m.easterEgg = mode
	if mode == easterEggMatrix {
		m.initMatrixColumns()
		// Generate first frame and start animation loop
		m.tickMatrix()
		return matrixTickCmd()
	}
	return nil
}

// dismissEasterEgg Dismiss current easter egg
func (m *cliModel) dismissEasterEgg() {
	m.easterEgg = easterEggNone
	m.matrixBuffer = nil
	m.matrixDrops = nil
	m.easterEggCustom = ""
}

// handleEasterEggCommand Handle hidden easter egg slash commands.
// Return (true, cmd) means command was handled by easter egg system, cmd needs to be executed by Bubble Tea.
// Return (false, nil) means it's not an easter egg command.
func (m *cliModel) handleEasterEggCommand(cmd string) (bool, tea.Cmd) {
	cmd = strings.TrimSpace(cmd)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false, nil
	}
	command := strings.ToLower(parts[0])

	switch command {
	case "/matrix":
		cmd := m.activateEasterEgg(easterEggMatrix)
		return true, cmd

	case "/sudo":
		m.appendSystem(randomSudoMessage())
		m.updateViewportContent()
		return true, nil

	case "/fortune":
		text, lucky := randomFortune()
		m.appendSystem(fmt.Sprintf("Fortune Cookie\n\n%s\n\nLucky number: %d", text, lucky))
		m.updateViewportContent()
		return true, nil

	case "/zen":
		haiku, message := randomZen()
		m.appendSystem(fmt.Sprintf("Zen Mode\n\n%s\n\n-- %s", haiku, message))
		m.updateViewportContent()
		return true, nil

	default:
		return false, nil
	}
}

// ---------------------------------------------------------------------------
// Easter egg rendering
// ---------------------------------------------------------------------------

// renderEasterEggOverlay Render easter egg overlay. Return empty string if no easter egg.
func (m *cliModel) renderEasterEggOverlay() string {
	switch m.easterEgg {
	case easterEggKonami:
		return m.renderKonamiOverlay()
	case easterEggMatrix:
		return m.renderMatrixOverlay()
	case easterEggAnswer42:
		return m.renderAnswer42Overlay()
	case easterEggVersion:
		return m.renderVersionOverlay()
	default:
		return ""
	}
}

// renderKonamiOverlay Render Konami Code celebration screen
func (m *cliModel) renderKonamiOverlay() string {
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Bold(true)
	content := green.Render(konamiASCII)
	return centerOverlay(content, m.width, m.height)
}

// renderMatrixOverlay Render Matrix rain screen
func (m *cliModel) renderMatrixOverlay() string {
	if m.matrixBuffer == nil {
		return ""
	}

	green := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	brightWhite := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	dimGreen := lipgloss.NewStyle().Foreground(lipgloss.Color("#003300"))

	var sb strings.Builder
	for r := 0; r < m.matrixRows; r++ {
		for c := 0; c < m.matrixCols; c++ {
			ch := m.matrixBuffer[r][c]
			if ch == ' ' {
				sb.WriteString(" ")
				continue
			}
			// Check if it's a column head
			isHead := false
			if m.matrixDrops != nil && c < len(m.matrixDrops) && m.matrixDrops[c] == r {
				isHead = true
			}
			if isHead {
				sb.WriteString(brightWhite.Render(string(ch)))
			} else {
				distance := 0
				if m.matrixDrops != nil && c < len(m.matrixDrops) {
					distance = m.matrixDrops[c] - r
					if distance < 0 {
						distance = 0
					}
				}
				if distance > 10 {
					sb.WriteString(dimGreen.Render(string(ch)))
				} else {
					sb.WriteString(green.Render(string(ch)))
				}
			}
		}
		if r < m.matrixRows-1 {
			sb.WriteString("\n")
		}
	}

	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Render("    Wake up, Neo... [ Press any key to exit ]")

	return centerOverlay(sb.String()+"\n\n"+hint, m.width, m.height)
}

// renderAnswer42Overlay Render "The Answer is 42" screen
func (m *cliModel) renderAnswer42Overlay() string {
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	content := yellow.Render(answer42Art)
	return centerOverlay(content, m.width, m.height)
}

// renderVersionOverlay Render version OCD achievement screen
func (m *cliModel) renderVersionOverlay() string {
	gold := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Bold(true)
	content := gold.Render(m.easterEggCustom)
	return centerOverlay(content, m.width, m.height)
}

// centerOverlay Center content in terminal of specified width and height
func centerOverlay(content string, termW, termH int) string {
	lines := strings.Split(content, "\n")
	maxW := 0
	for _, line := range lines {
		w := lipgloss.Width(line)
		if w > maxW {
			maxW = w
		}
	}

	padLeft := (termW - maxW) / 2
	if padLeft < 0 {
		padLeft = 0
	}
	padTop := (termH - len(lines)) / 2
	if padTop < 1 {
		padTop = 1
	}

	var sb strings.Builder
	for i := 0; i < padTop; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(strings.Repeat(" ", padLeft))
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// getHolidaySplashDesc Get holiday splash description text
func getHolidaySplashDesc() string {
	return holidaySplash()
}
