package main

import (
	"fmt"
	"os"

	"xbot/clipanic"
	"xbot/serverapp"
)

func main() {
	// 崩溃取证：把**任意 goroutine** 的未恢复 panic（以及运行时 fatal error）
	// 连同完整堆栈写进 $XBOT_HOME/logs/cli-panic.log。
	//
	// 2026-10-22 05:26 事故：serve 进程 exit status 2 崩掉，supervisor 只留下
	// "exited"，堆栈随 stderr 一起丢失，logs/cli-panic.log 从未生成 —— 根因是
	// 服务入口没启用文件日志，而 clipanic 当时也没有注册 debug.SetCrashOutput。
	// 必须在这里（早于一切 goroutine 启动）接线，否则下一次崩溃仍然无栈可查。
	clipanic.EnableFileLogging(clipanic.DefaultLogPath())

	if err := serverapp.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
