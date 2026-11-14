//go:build !race

package web

// raceEnabled 在普通构建下为 false（见 web_raceflag_test.go）。
const raceEnabled = false
