package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

const shutdownTimeout = 10 * time.Second

type config struct {
	authKey        string
	hostname       string
	stateDir       string
	listenAddr     string
	downstreamHost string
	downstreamPort int
	ephemeral      bool
	command        []string
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			slog.Error("downstream exited", "error", ee.err, "code", ee.code)
			os.Exit(ee.code)
		}
		slog.Error("tailgate stopped", "error", err)
		os.Exit(1)
	}
}

func parseConfig(args []string, getenv func(string) string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("tailgate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&cfg.authKey, "auth-key", getenv("TS_AUTHKEY"), "Tailscale auth key (default: TS_AUTHKEY)")
	fs.StringVar(&cfg.hostname, "hostname", getenv("TS_HOSTNAME"), "tailnet hostname (default: TS_HOSTNAME or OS hostname)")
	fs.StringVar(&cfg.stateDir, "state-dir", envOr(getenv, "TS_STATE_DIR", "/var/lib/tailgate"), "Tailscale state directory")
	fs.StringVar(&cfg.listenAddr, "listen", ":443", "tailnet HTTPS listen address")
	fs.StringVar(&cfg.downstreamHost, "downstream-host", "127.0.0.1", "downstream host")
	fs.IntVar(&cfg.downstreamPort, "downstream-port", 8080, "downstream HTTP port")
	fs.BoolVar(&cfg.ephemeral, "ephemeral", false, "register an ephemeral Tailscale node")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg.command = fs.Args()
	if cfg.authKey == "" {
		return cfg, errors.New("Tailscale auth key is required; set TS_AUTHKEY or --auth-key")
	}
	if cfg.downstreamPort < 1 || cfg.downstreamPort > 65535 {
		return cfg, fmt.Errorf("invalid downstream port %d", cfg.downstreamPort)
	}
	if cfg.hostname == "" {
		name, err := os.Hostname()
		if err != nil {
			return cfg, fmt.Errorf("determine hostname: %w", err)
		}
		cfg.hostname = name
	}
	return cfg, nil
}

func envOr(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func run(ctx context.Context, cfg config) error {
	child, childDone, err := startChild(cfg.command)
	if err != nil {
		return err
	}
	if child != nil {
		defer stopChild(child, childDone)
	}

	ts := &tsnet.Server{
		AuthKey:   cfg.authKey,
		Dir:       cfg.stateDir,
		Ephemeral: cfg.ephemeral,
		Hostname:  cfg.hostname,
		Logf: func(format string, args ...any) {
			slog.Debug(fmt.Sprintf(format, args...))
		},
	}
	defer ts.Close()

	listener, err := ts.ListenTLS("tcp", cfg.listenAddr)
	if err != nil {
		return fmt.Errorf("connect to Tailscale and listen for HTTPS: %w", err)
	}
	defer listener.Close()

	target, err := url.Parse("http://" + cfg.downstreamHost + ":" + strconv.Itoa(cfg.downstreamPort))
	if err != nil {
		return fmt.Errorf("build downstream URL: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		slog.Warn("downstream request failed", "error", proxyErr)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	httpServer := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	slog.Info("HTTPS proxy ready", "hostname", cfg.hostname, "listen", cfg.listenAddr, "downstream", target.String())

	select {
	case <-ctx.Done():
		shutdownHTTP(httpServer)
		return nil
	case waitErr := <-childDone:
		shutdownHTTP(httpServer)
		return childExitError(waitErr)
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTPS: %w", serveErr)
	}
}

func startChild(argv []string) (*exec.Cmd, <-chan error, error) {
	if len(argv) == 0 {
		return nil, nil, nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start downstream command: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	slog.Info("downstream command started", "pid", cmd.Process.Pid, "command", argv)
	return cmd, done, nil
}

func stopChild(cmd *exec.Cmd, done <-chan error) {
	select {
	case <-done:
		return
	default:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func childExitError(err error) error {
	if err == nil {
		return &exitError{code: 0, err: errors.New("downstream command exited")}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return &exitError{code: 128 + int(status.Signal()), err: err}
		}
		return &exitError{code: ee.ExitCode(), err: err}
	}
	return err
}

func shutdownHTTP(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Warn("HTTPS server did not shut down cleanly", "error", err)
	}
}
