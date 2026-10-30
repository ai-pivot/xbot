//go:build race

package web

// raceEnabled 在 -race 构建下为 true。用于跳过对墙钟敏感的断言：
// race detector 会把 SQLite 打开、auth 中间件等路径拖到数百毫秒
// （实测 register/login 各 650~700ms），此时「100–200ms 档」本身不可观测，
// 不是被测逻辑的问题。分档逻辑的严格断言由非 -race 的默认门禁覆盖。
const raceEnabled = true
