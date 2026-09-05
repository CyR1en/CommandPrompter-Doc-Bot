package database

import (
	"errors"
	"net/url"
	"os"
	"strconv"
)

// URLFromEnvironment preserves the existing DATABASE_URL/POSTGRES_* contract.
func URLFromEnvironment() (string, error) {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value, nil
	}

	password, ok := os.LookupEnv("POSTGRES_PASSWORD")
	if !ok || password == "" {
		return "", errors.New("DATABASE_URL or POSTGRES_PASSWORD is required")
	}

	port := 5432
	if value := os.Getenv("POSTGRES_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", errors.New("POSTGRES_PORT must be between 1 and 65535")
		}
		port = parsed
	}

	host := environmentOr("POSTGRES_HOST", "postgres")
	database := environmentOr("POSTGRES_DB", "ref0")
	username := environmentOr("POSTGRES_USER", "ref0")
	return (&url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(username, password),
		Host:   host + ":" + strconv.Itoa(port),
		Path:   "/" + database,
	}).String(), nil
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
