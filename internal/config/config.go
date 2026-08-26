package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr, DatabasePath                      string
	SessionTTL, ShutdownTimeout, WorkerInterval time.Duration
	WorkerBatch                                 int
}

func Load() (Config, error) {
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8080"), DatabasePath: env("DB_PATH", ".data/sports.db"), SessionTTL: 12 * time.Hour, ShutdownTimeout: 10 * time.Second, WorkerInterval: 2 * time.Second, WorkerBatch: 10}
	var err error
	if c.SessionTTL, err = duration("SESSION_TTL", c.SessionTTL); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", c.ShutdownTimeout); err != nil {
		return c, err
	}
	if c.WorkerInterval, err = duration("WORKER_INTERVAL", c.WorkerInterval); err != nil {
		return c, err
	}
	if raw := os.Getenv("WORKER_BATCH"); raw != "" {
		c.WorkerBatch, err = strconv.Atoi(raw)
		if err != nil || c.WorkerBatch < 1 {
			return c, fmt.Errorf("WORKER_BATCH must be positive")
		}
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return value, nil
}
