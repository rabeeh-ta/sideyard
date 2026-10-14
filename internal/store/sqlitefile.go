package store

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
)

var sqliteMagic = []byte("SQLite format 3\x00")

// IsSQLite reports whether path starts with the SQLite file header.
func IsSQLite(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, sqliteMagic)
}

// SnapshotFile writes a consistent copy of the SQLite database at src to
// dst (which must not exist) using VACUUM INTO. It's safe while another
// process is writing to src.
func SnapshotFile(src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`VACUUM INTO ?`, dst)
	return err
}

// CheckFile runs SQLite's quick integrity check on a database file.
func CheckFile(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}
