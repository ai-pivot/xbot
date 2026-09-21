// 临时探针（用完即删）：创建/更新一个已知口令的 web 用户，用于 curl 实证 SSE 事件载荷。
package main

import (
	"database/sql"
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
	_ "xbot/storage/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "/root/.xbot/xbot.db")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("tmp-pw-123"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	if _, err := db.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?) "+
			"ON CONFLICT(username) DO UPDATE SET password = excluded.password",
		"tmp-probe", string(hash),
	); err != nil {
		panic(err)
	}
	fmt.Println("tmp-probe ready (password tmp-pw-123)")
	os.Exit(0)
}
