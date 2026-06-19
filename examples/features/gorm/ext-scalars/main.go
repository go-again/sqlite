// gorm-ext-scalars: pair gorm with the scalar / aggregate / collation
// flavors of ext/. Every extension here registers itself on every conn
// the gorm pool opens via the `/auto` blank-import, then participates
// in plain gorm Where / Order / Select / aggregate calls.
//
// Covered: ext/regexp, ext/uuid, ext/hash, ext/ipaddr, ext/zorder,
// ext/stats, ext/unicode.
//
// Run with:
//
//	just example ext-scalars
package main

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"

	// Each blank import registers its module on every connection the
	// pool opens via Driver.ConnectHook. Plain gorm calls then see the
	// functions / operators / collations as if they were built in.
	_ "gosqlite.org/ext/hash/auto"
	_ "gosqlite.org/ext/ipaddr/auto"
	_ "gosqlite.org/ext/regexp/auto"
	_ "gosqlite.org/ext/stats/auto"
	_ "gosqlite.org/ext/unicode/auto"
	_ "gosqlite.org/ext/uuid/auto"
	_ "gosqlite.org/ext/zorder/auto"
)

// Event is a typical observability-style row that lets us showcase
// every scalar / aggregate / collation extension at once: text fields
// for regexp / unicode, an IP column for ipaddr, X/Y coords for
// zorder, a numeric column for stats, and a body the hash extension
// can digest.
type Event struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string
	SrcIP string
	X     int
	Y     int
	Cost  float64
}

func main() {
	db, err := gorm.Open(sqlitegorm.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&Event{}); err != nil {
		log.Fatal(err)
	}

	seed := []Event{
		{Title: "Login from alice", Body: "ok", SrcIP: "10.0.0.4", X: 3, Y: 1, Cost: 0.5},
		{Title: "LOGIN from bob", Body: "ok", SrcIP: "10.0.0.7", X: 5, Y: 8, Cost: 0.7},
		{Title: "Café visit", Body: "info", SrcIP: "192.168.1.20", X: 8, Y: 2, Cost: 1.3},
		{Title: "Network error", Body: "err", SrcIP: "172.16.0.5", X: 1, Y: 9, Cost: 4.1},
		{Title: "naïve retry", Body: "warn", SrcIP: "192.168.1.21", X: 6, Y: 6, Cost: 2.8},
	}
	if err := db.Create(&seed).Error; err != nil {
		log.Fatal(err)
	}

	// --- ext/regexp ---
	// The REGEXP operator + regexp_* scalars compose into Where the
	// same way LIKE does. Case-insensitive prefix here.
	var loginRows []Event
	if err := db.Where(`title REGEXP ?`, `(?i)^login`).Find(&loginRows).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[regexp]  %d rows matched (?i)^login\n", len(loginRows))

	// --- ext/uuid ---
	// Synthesise a v7 UUID per row right inside the query — useful for
	// API responses that need an opaque ID without changing the schema.
	var uuids []struct{ ID, Title string }
	if err := db.Raw(
		`SELECT uuid_str(uuid(7)) AS id, title FROM events ORDER BY id LIMIT 2`).
		Scan(&uuids).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[uuid]    v7 of row 1: %s … %q\n", uuids[0].ID, uuids[0].Title)

	// --- ext/hash ---
	// Project a SHA-256 of the body alongside the row.
	var digests []struct{ Title, Digest string }
	if err := db.Model(&Event{}).
		Select(`title, hex(sha256(body)) AS digest`).
		Order("id").Limit(2).
		Scan(&digests).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[hash]    sha256(%q) = %s…\n", digests[0].Title, digests[0].Digest[:16])

	// --- ext/ipaddr ---
	// ipcontains(cidr, ip) — only the 10.0.0.0/8 sources.
	var internal []Event
	if err := db.Where(`ipcontains(?, src_ip)`, "10.0.0.0/8").Find(&internal).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[ipaddr]  %d rows from 10.0.0.0/8\n", len(internal))

	// --- ext/zorder ---
	// Sort by the Morton code of (x, y) so spatially nearby rows cluster.
	var ordered []struct{ Title string }
	if err := db.Model(&Event{}).
		Select("title").Order("zorder(x, y)").Limit(3).
		Scan(&ordered).Error; err != nil {
		log.Fatal(err)
	}
	titles := make([]string, len(ordered))
	for i, r := range ordered {
		titles[i] = r.Title
	}
	fmt.Printf("[zorder]  first 3 by Morton(x,y): %s\n", strings.Join(titles, " | "))

	// --- ext/stats ---
	// p50 / p90 of cost via the (x, p) percentile_cont signature, plus
	// median for sanity. ext/stats also exposes the SQL-standard
	// percentile(x, p) where p is 0..100.
	var p struct{ Median, P50, P90 float64 }
	if err := db.Model(&Event{}).
		Select(`median(cost) AS median,
		        percentile_cont(cost, 0.5) AS p50,
		        percentile_cont(cost, 0.9) AS p90`).
		Scan(&p).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[stats]   cost median=%.2f p50=%.2f p90=%.2f\n", p.Median, p.P50, p.P90)

	// --- ext/unicode ---
	// NOCASE_UNICODE collation handles diacritics + case in one shot.
	// "cafe" matches "Café"; "naive" matches "naïve".
	var unicodeMatches []struct{ Title string }
	if err := db.Model(&Event{}).
		Select("title").
		Where("unaccent(lower(title)) = ?", "cafe visit").
		Scan(&unicodeMatches).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[unicode] unaccent('Café visit') match: %d row(s): %q\n",
		len(unicodeMatches), unicodeMatches[0].Title)
}
