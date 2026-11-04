// Package config loads JSON settings below explicitly supplied command-line flags.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

type secondsValue time.Duration

func (v *secondsValue) Set(raw string) error {
	if n, err := strconv.Atoi(raw); err == nil {
		*v = secondsValue(time.Duration(n) * time.Second)
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	*v = secondsValue(d)
	return nil
}

func (v *secondsValue) String() string { return time.Duration(*v).String() }

func (v *secondsValue) Get() any { return time.Duration(*v) }

// Seconds registers an interval flag whose numeric values are seconds.
// Duration strings are also accepted, preserving subsecond JSON intervals.
func Seconds(fs *flag.FlagSet, name string, seconds int, usage string) *time.Duration {
	value := time.Duration(seconds) * time.Second
	fs.Var((*secondsValue)(&value), name, usage)
	return &value
}

// FileFlags registers both aliases for the optional configuration file.
func FileFlags(fs *flag.FlagSet) *string {
	var path string
	fs.StringVar(&path, "c", "", "path to JSON configuration file")
	fs.StringVar(&path, "config", "", "path to JSON configuration file")
	return &path
}

// Apply loads the file selected by CONFIG or the flags and applies known fields.
// Explicit flags, including false, zero and empty values, override file values.
// Callers apply their environment overrides after this function returns.
func Apply(fs *flag.FlagSet, path string, fields map[string]string) error {
	if env := os.Getenv("CONFIG"); env != "" {
		path = env
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode config %q: %w", path, err)
	}
	if values == nil {
		return fmt.Errorf("config %q must contain a JSON object", path)
	}
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for field, name := range fields {
		raw, ok := values[field]
		if !ok || explicit[name] {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return fmt.Errorf("config field %s refers to unregistered flag %q", field, name)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("config field %s: %w", field, err)
		}
		if getter, ok := f.Value.(flag.Getter); ok {
			valid := false
			switch getter.Get().(type) {
			case string:
				_, valid = value.(string)
			case bool:
				_, valid = value.(bool)
			case int:
				_, valid = value.(float64)
			case time.Duration:
				switch value.(type) {
				case string, float64:
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("config field %s has an invalid type", field)
			}
		}
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case bool, float64:
			text = string(raw)
		default:
			return fmt.Errorf("config field %s must be a string, boolean or number", field)
		}
		if err := fs.Set(name, text); err != nil {
			return fmt.Errorf("config field %s: %w", field, err)
		}
	}
	return nil
}
