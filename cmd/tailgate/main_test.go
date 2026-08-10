package main

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestParseConfig(t *testing.T) {
	getenv := func(key string) string {
		values := map[string]string{"TS_AUTHKEY": "tskey-test", "TS_HOSTNAME": "demo"}
		return values[key]
	}
	cfg, err := parseConfig([]string{"--downstream-port", "3000", "--", "server", "--verbose"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.downstreamPort != 3000 || cfg.downstreamHost != "127.0.0.1" {
		t.Fatalf("unexpected downstream: %s:%d", cfg.downstreamHost, cfg.downstreamPort)
	}
	if want := []string{"server", "--verbose"}; !reflect.DeepEqual(cfg.command, want) {
		t.Fatalf("command = %q, want %q", cfg.command, want)
	}
}

func TestParseConfigRequiresAuthKey(t *testing.T) {
	_, err := parseConfig(nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseConfigRejectsInvalidPort(t *testing.T) {
	_, err := parseConfig([]string{"--auth-key", "x", "--downstream-port", "70000"}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestChildExitErrorPreservesExitCode(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 7").Run()
	got := childExitError(err)
	var exit *exitError
	if !errors.As(got, &exit) || exit.code != 7 {
		t.Fatalf("got %#v, want exit code 7", got)
	}
}
