package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                   int
	SQLiteDSN              string
	PixPendingThreshold    time.Duration
	BoletoPendingThreshold time.Duration
	AlertOrphanedThreshold int
	AlertGhostThreshold    int
	AlertHealthMin         float64
	ServerReadTimeout      time.Duration
	ServerWriteTimeout     time.Duration
	ServerIdleTimeout      time.Duration
	ShutdownTimeout        time.Duration
}

func Load() (Config, error) {
	var c Config
	var err error

	if c.Port, err = getInt("PORT", 8080); err != nil {
		return Config{}, err
	}
	c.SQLiteDSN = getString("SQLITE_DSN", "file:yuno.db?_pragma=journal_mode(WAL)")
	if c.PixPendingThreshold, err = getDuration("PIX_PENDING_THRESHOLD", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if c.BoletoPendingThreshold, err = getDuration("BOLETO_PENDING_THRESHOLD", 72*time.Hour); err != nil {
		return Config{}, err
	}
	if c.AlertOrphanedThreshold, err = getInt("ALERT_ORPHANED_THRESHOLD", 50); err != nil {
		return Config{}, err
	}
	if c.AlertGhostThreshold, err = getInt("ALERT_GHOST_THRESHOLD", 100); err != nil {
		return Config{}, err
	}
	if c.AlertHealthMin, err = getFloat("ALERT_HEALTH_MIN", 0.95); err != nil {
		return Config{}, err
	}
	if c.ServerReadTimeout, err = getDuration("SERVER_READ_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if c.ServerWriteTimeout, err = getDuration("SERVER_WRITE_TIMEOUT", 30*time.Second); err != nil {
		return Config{}, err
	}
	if c.ServerIdleTimeout, err = getDuration("SERVER_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return Config{}, err
	}
	if c.ShutdownTimeout, err = getDuration("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid PORT: %d out of range (1, 65535]", c.Port)
	}
	if c.PixPendingThreshold <= 0 {
		return fmt.Errorf("invalid PIX_PENDING_THRESHOLD: must be > 0")
	}
	if c.BoletoPendingThreshold <= 0 {
		return fmt.Errorf("invalid BOLETO_PENDING_THRESHOLD: must be > 0")
	}
	if c.ServerReadTimeout <= 0 {
		return fmt.Errorf("invalid SERVER_READ_TIMEOUT: must be > 0")
	}
	if c.ServerWriteTimeout <= 0 {
		return fmt.Errorf("invalid SERVER_WRITE_TIMEOUT: must be > 0")
	}
	if c.ServerIdleTimeout <= 0 {
		return fmt.Errorf("invalid SERVER_IDLE_TIMEOUT: must be > 0")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("invalid SHUTDOWN_TIMEOUT: must be > 0")
	}
	if c.AlertHealthMin < 0 || c.AlertHealthMin > 1 {
		return fmt.Errorf("invalid ALERT_HEALTH_MIN: %v out of range [0, 1]", c.AlertHealthMin)
	}
	if c.AlertOrphanedThreshold < 0 {
		return fmt.Errorf("invalid ALERT_ORPHANED_THRESHOLD: must be >= 0")
	}
	if c.AlertGhostThreshold < 0 {
		return fmt.Errorf("invalid ALERT_GHOST_THRESHOLD: must be >= 0")
	}
	return nil
}

func getString(env, def string) string {
	if v, ok := os.LookupEnv(env); ok {
		return v
	}
	return def
}

func getInt(env string, def int) (int, error) {
	v, ok := os.LookupEnv(env)
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", env, err)
	}
	return n, nil
}

func getDuration(env string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(env)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", env, err)
	}
	return d, nil
}

func getFloat(env string, def float64) (float64, error) {
	v, ok := os.LookupEnv(env)
	if !ok {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", env, err)
	}
	return f, nil
}
