package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApply(t *testing.T) {
	for _, alias := range []string{"c", "config"} {
		t.Run(alias, func(t *testing.T) {
			t.Setenv("CONFIG", "")
			path := writeConfig(t, `{"address":"file:8080","restore":true,"store_interval":"500ms","key":"secret"}`)
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			address := fs.String("a", "default:8080", "")
			restore := fs.Bool("r", true, "")
			key := fs.String("k", "default", "")
			interval := Seconds(fs, "i", 300, "")
			pathFlag := FileFlags(fs)
			if err := fs.Parse([]string{"-" + alias, path, "-r=false", "-k="}); err != nil {
				t.Fatal(err)
			}
			if err := Apply(fs, *pathFlag, map[string]string{"address": "a", "restore": "r", "store_interval": "i", "key": "k"}); err != nil {
				t.Fatal(err)
			}
			if *address != "file:8080" || *restore || *key != "" || *interval != 500*time.Millisecond {
				t.Fatalf("got address=%s restore=%v key=%q interval=%s", *address, *restore, *key, *interval)
			}
		})
	}
}

func TestApplyConfigEnvironment(t *testing.T) {
	t.Setenv("CONFIG", writeConfig(t, `{"address":"env-file:8080"}`))
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	address := fs.String("a", "default", "")
	if err := Apply(fs, "missing.json", map[string]string{"address": "a"}); err != nil {
		t.Fatal(err)
	}
	if *address != "env-file:8080" {
		t.Fatal(*address)
	}
}

func TestApplyErrors(t *testing.T) {
	t.Setenv("CONFIG", "")
	for _, body := range []string{`{`, `null`, `[]`, `{} {}`, `{"restore":{}}`, `{"restore":"bad"}`, `{"restore":1}`, `{"store_interval":"bad"}`, `{"store_interval":true}`, `{"store_interval":null}`, `{"store_interval":1.5}`} {
		t.Run(body, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.Bool("r", false, "")
			Seconds(fs, "i", 300, "")
			if err := Apply(fs, writeConfig(t, body), map[string]string{"restore": "r", "store_interval": "i"}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if err := Apply(flag.NewFlagSet("test", flag.ContinueOnError), filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("expected a read error")
	}
}

func TestApplyUnregisteredFlag(t *testing.T) {
	t.Setenv("CONFIG", "")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	err := Apply(fs, writeConfig(t, `{"address":"localhost:8080"}`), map[string]string{"address": "missing"})
	if err == nil || !strings.Contains(err.Error(), `unregistered flag "missing"`) {
		t.Fatalf("expected an unregistered flag error, got %v", err)
	}
}

func TestApplyInterval(t *testing.T) {
	t.Setenv("CONFIG", "")
	for _, raw := range []string{`2`, `"2s"`, `"500ms"`} {
		t.Run(raw, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			interval := Seconds(fs, "i", 300, "")
			if err := Apply(fs, writeConfig(t, `{"store_interval":`+raw+`}`), map[string]string{"store_interval": "i"}); err != nil {
				t.Fatal(err)
			}
			want := 2 * time.Second
			if raw == `"500ms"` {
				want = 500 * time.Millisecond
			}
			if *interval != want {
				t.Fatalf("got %s, want %s", *interval, want)
			}
		})
	}
}

func TestSeconds(t *testing.T) {
	for _, raw := range []string{"2", "2s", "500ms"} {
		t.Run(raw, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			interval := Seconds(fs, "i", 300, "")
			if err := fs.Parse([]string{"-i", raw}); err != nil {
				t.Fatal(err)
			}
			if *interval <= 0 || *interval == 300*time.Second {
				t.Fatal(*interval)
			}
		})
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
