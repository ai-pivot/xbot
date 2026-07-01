package config

import (
	"os"
	"strconv"
)

// setStringEnv reads an env var and writes it to dst if non-empty.
func setStringEnv(env string, dst *string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

// setIntEnv reads an env var, parses it as int, and writes it to dst if successful.
func setIntEnv(env string, dst *int) {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

// setBoolEnv reads an env var, parses it as bool, and writes it to dst if successful.
func setBoolEnv(env string, dst *bool) {
	if v := os.Getenv(env); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

// setFloatEnv reads an env var, parses it as float64, and writes it to dst if successful.
func setFloatEnv(env string, dst *float64) {
	if v := os.Getenv(env); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*dst = f
		}
	}
}
