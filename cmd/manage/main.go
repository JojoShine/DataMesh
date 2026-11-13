package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/platform/database"
)

func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "create-user" && os.Args[1] != "disable-user" && os.Args[1] != "restrict-test-user") {
		return fmt.Errorf("usage: manage create-user|disable-user|restrict-test-user < private-user.json")
	}
	var input identity.NewUser
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("invalid account input")
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("account input must contain one JSON object")
	}
	key, err := hex.DecodeString(os.Getenv("CSRF_KEY"))
	if err != nil {
		return fmt.Errorf("invalid configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("APP_DSN"))
	if err != nil {
		return fmt.Errorf("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer sqlDB.Close()
	service, err := identity.NewService(identity.NewRepository(db), key)
	if err != nil {
		return err
	}
	if os.Args[1] == "restrict-test-user" {
		if input.Username == "" || input.Password != "" || input.DepartmentID != "" || len(input.Platforms) > 0 || len(input.Roles) > 0 || input.PublicTest {
			return fmt.Errorf("restriction accepts only username")
		}
		if err := service.RestrictPublicTestUser(ctx, input.Username); err != nil {
			return fmt.Errorf("test account restriction failed")
		}
		fmt.Println("test identity restricted; previous sessions invalidated")
		return nil
	}
	if os.Args[1] == "disable-user" {
		if input.Password != "" || input.DepartmentID != "" || len(input.Platforms) != 0 || len(input.Roles) != 0 || input.PublicTest {
			return fmt.Errorf("disable-user accepts only username")
		}
		if err := service.DisableUser(ctx, input.Username); err != nil {
			return fmt.Errorf("account disable failed")
		}
		fmt.Println("account disabled; all sessions invalidated")
		return nil
	}
	id, err := service.CreateUser(ctx, input)
	input.Password = ""
	if err != nil {
		return fmt.Errorf("account creation failed: check input or existing username")
	}
	fmt.Println(id)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
