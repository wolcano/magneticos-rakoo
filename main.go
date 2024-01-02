//    magneticos searches torrents in a magnetico database, queries the current
//    number of seeders and lets the user select the one they want
//
//    Copyright (C) 2021 Matthieu Rakotojaona
//
//    This program is free software: you can redistribute it and/or modify
//    it under the terms of the GNU General Public License as published by
//    the Free Software Foundation, either version 3 of the License, or
//    (at your option) any later version.
//
//    This program is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//    GNU General Public License for more details.
//
//    You should have received a copy of the GNU General Public License
//    along with this program.  If not, see <https://www.gnu.org/licenses/>

// +build sqlite_fts5 fts5
package main

import (
	"fmt"
	"log"
	"database/sql"
	"sort"
	"context"
	"flag"
	"net/url"
	"os"

	appdirs "github.com/Wessie/appdirs"
	_ "github.com/mattn/go-sqlite3"
)

var fzfOutput *bool = flag.Bool("fzf-output", false, "if true, outputs the magnet, a TAB, and the rest. If false, outputs a human-readable output")
var queryInput *string = flag.String("query", "", "query to search for")
var dbPathInput *string = flag.String("dbpath", "", "path to database file")

func main() {

	flag.Parse()

	dbpath := getDBPath(*dbPathInput)
	if dbpath == "" {
		log.Fatal("missing db path")
	}

	u, err := url.Parse(dbpath)
	if err != nil {
		log.Fatal("specified db path [%s] is invalid: %s\n", dbpath, err)
	}
	_, err = os.Stat(u.Path)
	if os.IsNotExist(err) {
		log.Fatalf("specified database [%s] doesn't exist\n", dbpath)
	}

	db, err := sql.Open("sqlite3", dbpath)
	if err != nil {
		log.Fatalf("Couldn't open magnetico db: %s\n", err)
	}
	if *queryInput == "" {
		log.Fatal("need search argument")
	}
	defer db.Close()

	query := fmt.Sprintf("select name, hex(info_hash), total_size from torrents where rowid in (select rowid from torrents_idx where name match \"%s\")", *queryInput)
	rows, err := db.Query(query)
	if err != nil {
		log.Fatal("Query err: ", err)
	}
	defer rows.Close()

	entries := make([]entry, 0)

	for rows.Next() {
		var name, infohash string
		var size int
		err = rows.Scan(&name, &infohash, &size)
		if err != nil {
			log.Printf("err with scan: %s\n", err)
			continue
		}

		entries = append(entries, entry{
			infohash: infohash,
			name: name,
			size: size,
		})
	}
	
	err = rows.Err()
	if err != nil {
		log.Fatal("global err: ", err)
	}

	infohashes := make([]string, 0)
	for _, entry := range entries {
		infohashes = append(infohashes, entry.infohash)
	}

	log.Printf("%d rows\n---\n", len(entries))
	seeders := scrape(context.Background(), infohashes)
	for i := range entries {
		entries[i].seeders = seeders[i]
	}
	sort.Slice(entries, func(i, j int) bool {
		if *fzfOutput {
			return entries[i].seeders >= entries[j].seeders
		} else {
			return entries[i].seeders <= entries[j].seeders
		}
	})

	for _, entry := range entries {
		if *fzfOutput {
			fmt.Printf("%s\t%10s %4d seeders %s\n", magnetFrom(entry.infohash), humanize(entry.size), entry.seeders, entry.name)
		} else {
			fmt.Printf("\n  magnet = %s\n    name = %s\n    size = %s\n seeders = %d\n", magnetFrom(entry.infohash), entry.name, humanize(entry.size), entry.seeders)
		}
	}

	log.Println("---\ndone")
}

func getDBPath(dbPathInput string) (dbPath string) {
	if dbPathInput != "" {
		dbPath = dbPathInput
	} else {
		dbPath = appdirs.UserDataDir("magneticod", "", "", false) +
			"/database.sqlite3"
	}

	// Always set read-only
	u, err := url.Parse(dbPath)
	if err != nil {
		log.Println(err)
		return ""
	}
	values := u.Query()
	values.Set("mode", "ro")
	u.RawQuery = values.Encode()

	return u.String()
}

func humanize(size int) string {
	if size > 1024 * 1024 * 1024 * 1024 {
		return fmt.Sprintf("%.2f TiB", float32(size) / (1024 * 1024 * 1024 * 1024))
	} else if size > 1024 * 1024 * 1024 {
		return fmt.Sprintf("%.2f GiB", float32(size) / (1024 * 1024 * 1024))
	} else if size > 1024 * 1024 {
		return fmt.Sprintf("%.2f MiB", float32(size) / (1024 * 1024))
	} else if size > 1024 {
		return fmt.Sprintf("%.2f kiB", float32(size) / 1024)
	}

	return fmt.Sprintf("%d B", size)
}

type entry struct {
	infohash string
	size int
	name string
	seeders int
}
