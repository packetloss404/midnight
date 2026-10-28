//go:build ignore

// This fixture provisions existing local identities for existing-account
// acceptance tests. New-user invitation enrollment has its own browser journey
// through the public onboarding API. This helper grants no memberships.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/bootstrap"
)

func main() {
	if err := seed(); err != nil {
		// Connection errors can contain the DSN. Never print wrapped database or
		// credential errors in browser failure output.
		fmt.Fprintln(os.Stderr, "disposable browser identity fixture failed")
		os.Exit(1)
	}
}

func seed() error {
	var input struct {
		DSN   string `json:"dsn"`
		Users []struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			Password string `json:"password"`
		} `json:"users"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	u, err := url.Parse(input.DSN)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") ||
		u.Path != "/midnight_browser_e2e" {
		return fmt.Errorf("disposable DSN required")
	}
	for name, values := range u.Query() {
		if len(values) != 1 || (name != "sslmode" && name != "connect_timeout") ||
			(name == "sslmode" && values[0] != "disable") {
			return fmt.Errorf("connection override refused")
		}
		if name == "connect_timeout" {
			timeout, err := strconv.Atoi(values[0])
			if err != nil || timeout < 1 || timeout > 60 {
				return fmt.Errorf("connection timeout refused")
			}
		}
	}
	if len(input.Users) < 1 || len(input.Users) > 3 {
		return fmt.Errorf("bounded fixtures required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, input.DSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var claimed bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE is_admin=TRUE AND is_disabled=FALSE)`).Scan(&claimed); err != nil || !claimed {
		return fmt.Errorf("claim administrator first")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, user := range input.Users {
		if !regexp.MustCompile(`^usr_browser_[a-z0-9_]+$`).MatchString(user.ID) ||
			!strings.HasSuffix(user.Email, "@example.test") {
			return fmt.Errorf("fixture identity required")
		}
		hash, err := bootstrap.HashPassword(user.Password)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users
			(id,email,display_name,password_hash,session_generation,is_admin,created_at,updated_at)
			VALUES ($1,$2,'Disposable browser recipient',$3,1,FALSE,NOW(),NOW())`, user.ID, user.Email, hash); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
