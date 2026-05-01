package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

var allEnvVars = []string{
	"PORT",
	"SQLITE_DSN",
	"PIX_PENDING_THRESHOLD",
	"BOLETO_PENDING_THRESHOLD",
	"ALERT_ORPHANED_THRESHOLD",
	"ALERT_GHOST_THRESHOLD",
	"ALERT_HEALTH_MIN",
	"SERVER_READ_TIMEOUT",
	"SERVER_WRITE_TIMEOUT",
	"SERVER_IDLE_TIMEOUT",
	"SHUTDOWN_TIMEOUT",
}

// clearEnv ensures none of the config env vars are set during the test.
// t.Setenv snapshots the original value and registers cleanup; os.Unsetenv
// then actually removes the variable so LookupEnv reports it as absent.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allEnvVars {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if c.Port != 8080 {
		t.Errorf("Port = %d, want 8080", c.Port)
	}
	if c.SQLiteDSN != "file:yuno.db?_pragma=journal_mode(WAL)" {
		t.Errorf("SQLiteDSN = %q", c.SQLiteDSN)
	}
	if c.PixPendingThreshold != 24*time.Hour {
		t.Errorf("PixPendingThreshold = %v, want 24h", c.PixPendingThreshold)
	}
	if c.BoletoPendingThreshold != 72*time.Hour {
		t.Errorf("BoletoPendingThreshold = %v, want 72h", c.BoletoPendingThreshold)
	}
	if c.AlertOrphanedThreshold != 50 {
		t.Errorf("AlertOrphanedThreshold = %d, want 50", c.AlertOrphanedThreshold)
	}
	if c.AlertGhostThreshold != 100 {
		t.Errorf("AlertGhostThreshold = %d, want 100", c.AlertGhostThreshold)
	}
	if c.AlertHealthMin != 0.95 {
		t.Errorf("AlertHealthMin = %v, want 0.95", c.AlertHealthMin)
	}
	if c.ServerReadTimeout != 15*time.Second {
		t.Errorf("ServerReadTimeout = %v, want 15s", c.ServerReadTimeout)
	}
	if c.ServerWriteTimeout != 30*time.Second {
		t.Errorf("ServerWriteTimeout = %v, want 30s", c.ServerWriteTimeout)
	}
	if c.ServerIdleTimeout != 60*time.Second {
		t.Errorf("ServerIdleTimeout = %v, want 60s", c.ServerIdleTimeout)
	}
	if c.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", c.ShutdownTimeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		value string
		check func(t *testing.T, c Config)
	}{
		{
			name: "PORT", env: "PORT", value: "9090",
			check: func(t *testing.T, c Config) {
				if c.Port != 9090 {
					t.Errorf("Port = %d, want 9090", c.Port)
				}
			},
		},
		{
			name: "SQLITE_DSN", env: "SQLITE_DSN", value: "file::memory:?cache=shared",
			check: func(t *testing.T, c Config) {
				if c.SQLiteDSN != "file::memory:?cache=shared" {
					t.Errorf("SQLiteDSN = %q", c.SQLiteDSN)
				}
			},
		},
		{
			name: "PIX_PENDING_THRESHOLD", env: "PIX_PENDING_THRESHOLD", value: "1h30m",
			check: func(t *testing.T, c Config) {
				if c.PixPendingThreshold != 90*time.Minute {
					t.Errorf("PixPendingThreshold = %v", c.PixPendingThreshold)
				}
			},
		},
		{
			name: "BOLETO_PENDING_THRESHOLD", env: "BOLETO_PENDING_THRESHOLD", value: "48h",
			check: func(t *testing.T, c Config) {
				if c.BoletoPendingThreshold != 48*time.Hour {
					t.Errorf("BoletoPendingThreshold = %v", c.BoletoPendingThreshold)
				}
			},
		},
		{
			name: "ALERT_ORPHANED_THRESHOLD", env: "ALERT_ORPHANED_THRESHOLD", value: "200",
			check: func(t *testing.T, c Config) {
				if c.AlertOrphanedThreshold != 200 {
					t.Errorf("AlertOrphanedThreshold = %d", c.AlertOrphanedThreshold)
				}
			},
		},
		{
			name: "ALERT_GHOST_THRESHOLD", env: "ALERT_GHOST_THRESHOLD", value: "0",
			check: func(t *testing.T, c Config) {
				if c.AlertGhostThreshold != 0 {
					t.Errorf("AlertGhostThreshold = %d", c.AlertGhostThreshold)
				}
			},
		},
		{
			name: "ALERT_HEALTH_MIN", env: "ALERT_HEALTH_MIN", value: "0.5",
			check: func(t *testing.T, c Config) {
				if c.AlertHealthMin != 0.5 {
					t.Errorf("AlertHealthMin = %v", c.AlertHealthMin)
				}
			},
		},
		{
			name: "SERVER_READ_TIMEOUT", env: "SERVER_READ_TIMEOUT", value: "5s",
			check: func(t *testing.T, c Config) {
				if c.ServerReadTimeout != 5*time.Second {
					t.Errorf("ServerReadTimeout = %v", c.ServerReadTimeout)
				}
			},
		},
		{
			name: "SERVER_WRITE_TIMEOUT", env: "SERVER_WRITE_TIMEOUT", value: "10s",
			check: func(t *testing.T, c Config) {
				if c.ServerWriteTimeout != 10*time.Second {
					t.Errorf("ServerWriteTimeout = %v", c.ServerWriteTimeout)
				}
			},
		},
		{
			name: "SERVER_IDLE_TIMEOUT", env: "SERVER_IDLE_TIMEOUT", value: "120s",
			check: func(t *testing.T, c Config) {
				if c.ServerIdleTimeout != 120*time.Second {
					t.Errorf("ServerIdleTimeout = %v", c.ServerIdleTimeout)
				}
			},
		},
		{
			name: "SHUTDOWN_TIMEOUT", env: "SHUTDOWN_TIMEOUT", value: "30s",
			check: func(t *testing.T, c Config) {
				if c.ShutdownTimeout != 30*time.Second {
					t.Errorf("ShutdownTimeout = %v", c.ShutdownTimeout)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			c, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			tc.check(t, c)
		})
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		value     string
		wantInMsg string
	}{
		{name: "invalid PORT not int", env: "PORT", value: "abc", wantInMsg: "PORT"},
		{name: "PORT below range", env: "PORT", value: "0", wantInMsg: "PORT"},
		{name: "PORT above range", env: "PORT", value: "70000", wantInMsg: "PORT"},
		{name: "PORT negative", env: "PORT", value: "-1", wantInMsg: "PORT"},
		{name: "invalid PIX threshold", env: "PIX_PENDING_THRESHOLD", value: "notaduration", wantInMsg: "PIX_PENDING_THRESHOLD"},
		{name: "PIX threshold zero", env: "PIX_PENDING_THRESHOLD", value: "0s", wantInMsg: "PIX_PENDING_THRESHOLD"},
		{name: "PIX threshold negative", env: "PIX_PENDING_THRESHOLD", value: "-1h", wantInMsg: "PIX_PENDING_THRESHOLD"},
		{name: "invalid BOLETO threshold", env: "BOLETO_PENDING_THRESHOLD", value: "x", wantInMsg: "BOLETO_PENDING_THRESHOLD"},
		{name: "ALERT_HEALTH_MIN > 1", env: "ALERT_HEALTH_MIN", value: "1.5", wantInMsg: "ALERT_HEALTH_MIN"},
		{name: "ALERT_HEALTH_MIN < 0", env: "ALERT_HEALTH_MIN", value: "-0.1", wantInMsg: "ALERT_HEALTH_MIN"},
		{name: "ALERT_HEALTH_MIN invalid float", env: "ALERT_HEALTH_MIN", value: "abc", wantInMsg: "ALERT_HEALTH_MIN"},
		{name: "ALERT_ORPHANED negative", env: "ALERT_ORPHANED_THRESHOLD", value: "-1", wantInMsg: "ALERT_ORPHANED_THRESHOLD"},
		{name: "ALERT_GHOST negative", env: "ALERT_GHOST_THRESHOLD", value: "-5", wantInMsg: "ALERT_GHOST_THRESHOLD"},
		{name: "invalid SERVER_READ_TIMEOUT", env: "SERVER_READ_TIMEOUT", value: "x", wantInMsg: "SERVER_READ_TIMEOUT"},
		{name: "SERVER_WRITE_TIMEOUT zero", env: "SERVER_WRITE_TIMEOUT", value: "0s", wantInMsg: "SERVER_WRITE_TIMEOUT"},
		{name: "SHUTDOWN_TIMEOUT zero", env: "SHUTDOWN_TIMEOUT", value: "0s", wantInMsg: "SHUTDOWN_TIMEOUT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() expected error for %s=%q, got nil", tc.env, tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantInMsg)
			}
		})
	}
}
