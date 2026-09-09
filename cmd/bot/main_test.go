package main

import (
	"net/url"
	"testing"
)

func TestDatabaseDSNEscapesCredentialsAndPreservesQuery(t *testing.T) {
	dsn, err := databaseDSN(
		"postgresql://postgres:5432/cs2predictor?sslmode=disable",
		"user+deploy@example",
		"p@ss&word?#/%",
	)
	if err != nil {
		t.Fatalf("databaseDSN: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse generated DSN: %v", err)
	}
	if got := parsed.User.Username(); got != "user+deploy@example" {
		t.Fatalf("username = %q", got)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ss&word?#/%" {
		t.Fatalf("password did not round-trip")
	}
	if got := parsed.Query().Get("sslmode"); got != "disable" {
		t.Fatalf("sslmode = %q, want disable", got)
	}
}

func TestDatabaseDSNRejectsNonPostgresURL(t *testing.T) {
	if _, err := databaseDSN("https://db.example/cs2predictor", "user", "password"); err == nil {
		t.Fatal("expected non-Postgres DATABASE_URL to be rejected")
	}
}
