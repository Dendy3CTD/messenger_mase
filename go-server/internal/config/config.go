// Package config resolves the server settings from command-line flags,
// environment variables and built-in defaults.
//
// Precedence, per setting: an explicitly passed flag, then the environment
// variable, then the default. A flag counts as explicit even when its value
// equals the default, so a stale environment variable can never silently
// override something that was typed by hand.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Environment variable names.
const (
	EnvAddr     = "MASE_ADDR"
	EnvDB       = "MASE_DB"
	EnvMedia    = "MASE_MEDIA"
	EnvLogLevel = "MASE_LOG_LEVEL"
)

// Defaults used when neither a flag nor an environment variable is given.
const (
	DefaultAddr     = ":8080"
	DefaultDB       = "mase.sqlite"
	DefaultMedia    = "./media"
	DefaultLogLevel = "info"
)

// Source says where a resolved value came from.
type Source string

const (
	SourceDefault Source = "default"
	SourceEnv     Source = "env"
	SourceFlag    Source = "flag"
)

// Config holds the resolved server settings.
type Config struct {
	Addr  string // HTTP listen address
	DB    string // SQLite database path
	Media string // media storage directory
	// LogLevel is one of debug, info, warn, error. It is validated here;
	// level filtering itself arrives with structured logging.
	LogLevel string

	// Sources maps a setting name (addr, db, media, log-level) to its origin.
	Sources map[string]Source
}

var validLogLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// Load parses args (without the program name) and resolves every setting.
// getenv is os.Getenv in production. Help output for -h is written to out and
// reported as flag.ErrHelp.
func Load(args []string, getenv func(string) string, out io.Writer) (*Config, error) {
	fs := flag.NewFlagSet("mase-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fAddr := fs.String("addr", DefaultAddr, "HTTP listen address (env "+EnvAddr+")")
	fDB := fs.String("db", DefaultDB, "SQLite database path (env "+EnvDB+")")
	fMedia := fs.String("media", DefaultMedia, "media storage directory (env "+EnvMedia+")")
	fLevel := fs.String("log-level", DefaultLogLevel, "log level: debug, info, warn, error (env "+EnvLogLevel+")")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Usage: mase-server [flags]")
			fmt.Fprintln(out, "A flag given on the command line overrides its environment variable, which overrides the default.")
			fs.SetOutput(out)
			fs.PrintDefaults()
		}
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	cfg := &Config{Sources: map[string]Source{}}

	resolve := func(name, envName, def string, flagVal *string) (string, error) {
		var val, origin string
		switch v := strings.TrimSpace(getenv(envName)); {
		case explicit[name]:
			val, origin = strings.TrimSpace(*flagVal), "flag -"+name
			cfg.Sources[name] = SourceFlag
		case v != "":
			val, origin = v, "env "+envName
			cfg.Sources[name] = SourceEnv
		default:
			val, origin = def, "default"
			cfg.Sources[name] = SourceDefault
		}
		if val == "" {
			return "", fmt.Errorf("%s: value must not be empty", origin)
		}
		return val, nil
	}

	var err error
	if cfg.Addr, err = resolve("addr", EnvAddr, DefaultAddr, fAddr); err != nil {
		return nil, err
	}
	if cfg.DB, err = resolve("db", EnvDB, DefaultDB, fDB); err != nil {
		return nil, err
	}
	if cfg.Media, err = resolve("media", EnvMedia, DefaultMedia, fMedia); err != nil {
		return nil, err
	}
	level, err := resolve("log-level", EnvLogLevel, DefaultLogLevel, fLevel)
	if err != nil {
		return nil, err
	}
	level = strings.ToLower(level)
	if !validLogLevels[level] {
		origin := "flag -log-level"
		switch cfg.Sources["log-level"] {
		case SourceEnv:
			origin = "env " + EnvLogLevel
		case SourceDefault:
			origin = "default"
		}
		return nil, fmt.Errorf("%s: invalid log level %q (want debug, info, warn or error)", origin, level)
	}
	cfg.LogLevel = level

	return cfg, nil
}
