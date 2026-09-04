package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:.rundata/cloudpost.db?_pragma=busy_timeout(5000)")
	if err != nil { fmt.Println(err); return }
	defer db.Close()
	rows, _ := db.Query("SELECT id, subject, flags FROM messages ORDER BY id")
	defer rows.Close()
	for rows.Next() {
		var id int64
		var subj, flags string
		rows.Scan(&id, &subj, &flags)
		fmt.Printf("id=%d subj=%q flags=%q\n", id, subj, flags)
	}
	fr, _ := db.Query("SELECT id, name, cond_field, cond_op, cond_value, action, enabled FROM filters")
	defer fr.Close()
	for fr.Next() {
		var id int64
		var name, cf, co, cv, act string
		var en int64
		fr.Scan(&id, &name, &cf, &co, &cv, &act, &en)
		fmt.Printf("filter id=%d name=%q %s %s %q -> %s enabled=%d\n", id, name, cf, co, cv, act, en)
	}
}
