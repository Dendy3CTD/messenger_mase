package config

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		env         map[string]string
		wantAddr    string
		wantDB      string
		wantMedia   string
		wantLevel   string
		wantSources map[string]Source
	}{
		{
			name:      "defaults when nothing is set",
			wantAddr:  DefaultAddr,
			wantDB:    DefaultDB,
			wantMedia: DefaultMedia,
			wantLevel: DefaultLogLevel,
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceDefault, "media": SourceDefault, "log-level": SourceDefault,
			},
		},
		{
			name: "environment overrides defaults",
			env: map[string]string{
				EnvAddr: ":9000", EnvDB: "/data/env.sqlite", EnvMedia: "/data/env-media", EnvLogLevel: "debug",
			},
			wantAddr: ":9000", wantDB: "/data/env.sqlite", wantMedia: "/data/env-media", wantLevel: "debug",
			wantSources: map[string]Source{
				"addr": SourceEnv, "db": SourceEnv, "media": SourceEnv, "log-level": SourceEnv,
			},
		},
		{
			name:     "explicit flag beats environment",
			args:     []string{"-db", "/tmp/flag.sqlite"},
			env:      map[string]string{EnvDB: "/data/forgotten-prod.sqlite"},
			wantAddr: DefaultAddr, wantDB: "/tmp/flag.sqlite", wantMedia: DefaultMedia, wantLevel: DefaultLogLevel,
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceFlag, "media": SourceDefault, "log-level": SourceDefault,
			},
		},
		{
			name:     "explicit flag equal to the default still beats environment",
			args:     []string{"-addr", DefaultAddr},
			env:      map[string]string{EnvAddr: ":9999"},
			wantAddr: DefaultAddr, wantDB: DefaultDB, wantMedia: DefaultMedia, wantLevel: DefaultLogLevel,
			wantSources: map[string]Source{
				"addr": SourceFlag, "db": SourceDefault, "media": SourceDefault, "log-level": SourceDefault,
			},
		},
		{
			name:     "flags and environment are resolved per setting",
			args:     []string{"-media", "/tmp/m"},
			env:      map[string]string{EnvDB: "/data/env.sqlite"},
			wantAddr: DefaultAddr, wantDB: "/data/env.sqlite", wantMedia: "/tmp/m", wantLevel: DefaultLogLevel,
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceEnv, "media": SourceFlag, "log-level": SourceDefault,
			},
		},
		{
			name:     "empty and blank environment values are treated as unset",
			env:      map[string]string{EnvAddr: "", EnvDB: "   "},
			wantAddr: DefaultAddr, wantDB: DefaultDB, wantMedia: DefaultMedia, wantLevel: DefaultLogLevel,
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceDefault, "media": SourceDefault, "log-level": SourceDefault,
			},
		},
		{
			name:     "log level is case-insensitive and normalised",
			env:      map[string]string{EnvLogLevel: "WARN"},
			wantAddr: DefaultAddr, wantDB: DefaultDB, wantMedia: DefaultMedia, wantLevel: "warn",
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceDefault, "media": SourceDefault, "log-level": SourceEnv,
			},
		},
		{
			name:     "log level flag beats environment",
			args:     []string{"-log-level", "error"},
			env:      map[string]string{EnvLogLevel: "debug"},
			wantAddr: DefaultAddr, wantDB: DefaultDB, wantMedia: DefaultMedia, wantLevel: "error",
			wantSources: map[string]Source{
				"addr": SourceDefault, "db": SourceDefault, "media": SourceDefault, "log-level": SourceFlag,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(tc.args, env(tc.env), &bytes.Buffer{})
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.Addr != tc.wantAddr || cfg.DB != tc.wantDB || cfg.Media != tc.wantMedia || cfg.LogLevel != tc.wantLevel {
				t.Errorf("got addr=%q db=%q media=%q level=%q; want addr=%q db=%q media=%q level=%q",
					cfg.Addr, cfg.DB, cfg.Media, cfg.LogLevel, tc.wantAddr, tc.wantDB, tc.wantMedia, tc.wantLevel)
			}
			for k, want := range tc.wantSources {
				if got := cfg.Sources[k]; got != want {
					t.Errorf("source[%s] = %q, want %q", k, got, want)
				}
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"invalid log level from env", nil, map[string]string{EnvLogLevel: "verbose"}, EnvLogLevel},
		{"invalid log level from flag", []string{"-log-level", "loud"}, nil, "-log-level"},
		{"invalid flag log level is not rescued by valid env", []string{"-log-level", "loud"}, map[string]string{EnvLogLevel: "info"}, "loud"},
		{"explicitly empty flag is rejected, not replaced by env", []string{"-db", ""}, map[string]string{EnvDB: "/data/x.sqlite"}, "-db"},
		{"unknown flag", []string{"-nope"}, nil, "nope"},
		{"unexpected positional argument", []string{"stray"}, nil, "stray"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(tc.args, env(tc.env), &bytes.Buffer{})
			if err == nil {
				t.Fatalf("Load() = %+v, want error containing %q", cfg, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadHelp(t *testing.T) {
	var out bytes.Buffer
	_, err := Load([]string{"-h"}, env(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{EnvAddr, EnvDB, EnvMedia, EnvLogLevel, "-db"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage output lacks %q:\n%s", want, out.String())
		}
	}
}
