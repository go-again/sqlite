// gorm example: shows the minimal change to migrate from
// github.com/Tryanks/gorm-sqlite (or glebarez/sqlite) to
// github.com/go-again/sqlite/gorm. The sqlite.Open(dsn) call signature is
// identical; the package name remains `sqlite` so the rest of your code
// keeps compiling.
package main

import (
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sqlite "github.com/go-again/sqlite/gorm"
)

type User struct {
	ID    uint   `gorm:"primaryKey"`
	Name  string
	Email string `gorm:"uniqueIndex"`
}

func main() {
	db, err := gorm.Open(sqlite.Open("file:gorm-demo?mode=memory&cache=shared&_pragma=foreign_keys(1)"),
		&gorm.Config{
			Logger:         logger.Default.LogMode(logger.Warn),
			TranslateError: true,
		})
	if err != nil {
		log.Fatal(err)
	}

	if err := db.AutoMigrate(&User{}); err != nil {
		log.Fatal(err)
	}

	if err := db.Create(&User{Name: "alice", Email: "a@example.com"}).Error; err != nil {
		log.Fatal(err)
	}

	// Unique-constraint violations translate to gorm.ErrDuplicatedKey when
	// TranslateError is enabled — same behavior as glebarez/sqlite.
	err = db.Create(&User{Name: "alice2", Email: "a@example.com"}).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		fmt.Println("dup email, as expected:", err)
	}

	var got User
	if err := db.First(&got, "email = ?", "a@example.com").Error; err != nil {
		log.Fatal(err)
	}
	fmt.Println("found:", got.ID, got.Name, got.Email)
}
