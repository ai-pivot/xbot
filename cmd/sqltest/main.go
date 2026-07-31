package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
)

func main() {
	dbPath := "testdb.sqlite"
	if len(os.Args) > 1 && os.Args[1] == "write" {
		os.Remove(dbPath)
		os.Remove(dbPath + "-wal")
		os.Remove(dbPath + "-shm")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			fmt.Println("OPEN_ERROR:", err)
			os.Exit(1)
		}

		_, err = db.Exec("PRAGMA journal_mode=WAL")
		if err != nil {
			fmt.Println("WAL_ERROR:", err)
			os.Exit(1)
		}

		// Create many tables and data to force large WAL (>1MB, depth=1 extent tree)
		for i := 0; i < 62; i++ {
			tbl := fmt.Sprintf("table_%02d", i)
			_, err = db.Exec(fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, data BLOB)", tbl))
			if err != nil {
				fmt.Printf("CREATE_ERROR: %v\n", err)
				os.Exit(1)
			}
		}
		big := make([]byte, 4000)
		for i := range big {
			big[i] = byte(i % 256)
		}
		for i := 0; i < 62; i++ {
			tbl := fmt.Sprintf("table_%02d", i)
			for j := 0; j < 20; j++ {
				_, err = db.Exec(fmt.Sprintf("INSERT INTO %s (data) VALUES (?)", tbl), big)
				if err != nil {
					fmt.Printf("INSERT_ERROR: %v\n", err)
					os.Exit(1)
				}
			}
		}
		// Do updates to force WAL page overwrites (like xbot does)
		for i := 0; i < 62; i++ {
			tbl := fmt.Sprintf("table_%02d", i)
			_, err = db.Exec(fmt.Sprintf("UPDATE %s SET data = ? WHERE id = 1", tbl), []byte("UPDATED_DATA"))
			if err != nil {
				fmt.Printf("UPDATE_ERROR: %v\n", err)
				os.Exit(1)
			}
		}
		_, err = db.Exec("INSERT INTO table_00 (data) VALUES (?)", []byte("CHECK_ROW"))
		if err != nil {
			fmt.Println("CHECK_ERROR:", err)
			os.Exit(1)
		}

		fmt.Println("WRITE_OK")
		os.Exit(0) // Force exit without db.Close (like xbot)
	} else {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			fmt.Println("OPEN_ERROR:", err)
			os.Exit(1)
		}
		defer db.Close()

		var tables int
		db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables)
		var rows int
		db.QueryRow("SELECT count(*) FROM table_00").Scan(&rows)
		var updated int
		db.QueryRow("SELECT count(*) FROM table_00 WHERE data = 'UPDATED_DATA'").Scan(&updated)
		var checkRow int
		db.QueryRow("SELECT count(*) FROM table_00 WHERE data = 'CHECK_ROW'").Scan(&checkRow)

		fmt.Printf("READ tables=%d rows_t00=%d updated=%d check=%d\n", tables, rows, updated, checkRow)
		if tables == 0 {
			fmt.Println("READ_FAIL")
			os.Exit(1)
		}
		fmt.Println("READ_OK")
	}
}
