package config

import (
	"testing"
	"time"
)

func TestLoadUsesOperationalDefaults(t *testing.T) {
	for _, key := range []string{"HTTP_ADDR", "DB_PATH", "SESSION_TTL", "SHUTDOWN_TIMEOUT", "WORKER_INTERVAL", "WORKER_BATCH"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr=%q", cfg.HTTPAddr)
	}
	if cfg.DatabasePath != ".data/sports.db" {
		t.Errorf("DatabasePath=%q", cfg.DatabasePath)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Errorf("SessionTTL=%s", cfg.SessionTTL)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout=%s", cfg.ShutdownTimeout)
	}
	if cfg.WorkerInterval != 2*time.Second {
		t.Errorf("WorkerInterval=%s", cfg.WorkerInterval)
	}
	if cfg.WorkerBatch != 10 {
		t.Errorf("WorkerBatch=%d", cfg.WorkerBatch)
	}
}

func TestLoadAcceptsExplicitConfiguration(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("DB_PATH", "/tmp/config-test.db")
	t.Setenv("SESSION_TTL", "45m")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("WORKER_INTERVAL", "250ms")
	t.Setenv("WORKER_BATCH", "7")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.DatabasePath != "/tmp/config-test.db" {
		t.Fatalf("string settings not loaded: %+v", cfg)
	}
	if cfg.SessionTTL != 45*time.Minute || cfg.ShutdownTimeout != 3*time.Second || cfg.WorkerInterval != 250*time.Millisecond {
		t.Fatalf("duration settings not loaded: %+v", cfg)
	}
	if cfg.WorkerBatch != 7 {
		t.Fatalf("WorkerBatch=%d", cfg.WorkerBatch)
	}
}

func TestLoadRejectsInvalidDurations(t *testing.T) {
	cases := []struct{ key, value string }{{"SESSION_TTL", "later"}, {"SESSION_TTL", "0s"}, {"SHUTDOWN_TIMEOUT", "-1s"}, {"WORKER_INTERVAL", "0"}}
	for _, tc := range cases {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			for _, key := range []string{"SESSION_TTL", "SHUTDOWN_TIMEOUT", "WORKER_INTERVAL", "WORKER_BATCH"} {
				t.Setenv(key, "")
			}
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestLoadRejectsInvalidWorkerBatch(t *testing.T) {
	for _, value := range []string{"0", "-2", "many"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("WORKER_BATCH", value)
			if _, err := Load(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
