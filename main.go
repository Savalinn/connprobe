// Copyright (c) 2026 Gabor Puskas
// SPDX-License-Identifier: MIT

// connprobe periodically opens TCP connections to every port of every
// target address and logs each finished attempt as one JSON line.
//
// A single central ticker drives all probes: on every tick one attempt is
// started for each host×port pair, so all targets share the same schedule.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	minIntervalMs = 400
	maxHosts      = 10
)

// Result is one finished probe, written as a single JSON line.
type Result struct {
	Seq       uint64  `json:"seq"`
	Host      string  `json:"host"`
	IP        string  `json:"ip"`
	Port      int     `json:"port"`
	Start     string  `json:"start"`      // RFC3339, microsecond precision, local TZ
	StartUs   int64   `json:"start_us"`   // Unix epoch in microseconds
	End       string  `json:"end"`        // when the outcome was known
	Outcome   string  `json:"outcome"`    // success | closed | timeout | error
	ElapsedMs float64 `json:"elapsed_ms"` // microsecond resolution
	Error     string  `json:"error,omitempty"`
}

const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

// config holds the validated command-line settings.
type config struct {
	targets  []target
	ports    []int
	interval time.Duration
	timeout  time.Duration
	logPath  string
	count    int
	quiet    bool
}

// argError is an invalid argument value detected after flag parsing.
type argError struct{ msg string }

func (e *argError) Error() string { return e.msg }

func argErrorf(format string, args ...any) error {
	return &argError{fmt.Sprintf(format, args...)}
}

// parseArgs parses and validates the command line. Flag syntax errors and
// -h are reported to out by the flag package itself.
func parseArgs(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("connprobe", flag.ContinueOnError)
	fs.SetOutput(out)
	hostsArg := fs.String("hosts", "", fmt.Sprintf("comma separated target IPs (IPv4/IPv6), max %d, e.g. 192.168.0.4,2001:db8::1 (required)", maxHosts))
	portsArg := fs.String("ports", "", "comma separated port list, e.g. 22,80,443 (required)")
	intervalMs := fs.Int("interval", 1000, fmt.Sprintf("milliseconds between probe rounds (min %d)", minIntervalMs))
	timeoutMs := fs.Int("timeout", 1000, "connect timeout in milliseconds")
	logPath := fs.String("log", "", "path of the JSON-lines log file, appended to (required)")
	count := fs.Int("count", 0, "number of rounds, 0 = run until interrupted")
	quiet := fs.Bool("quiet", false, "do not echo results to stdout")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, argErrorf("unexpected argument %q", fs.Arg(0))
	}

	targets, err := parseHosts(*hostsArg)
	if err != nil {
		return config{}, argErrorf("-hosts: %v", err)
	}
	ports, err := parsePorts(*portsArg)
	if err != nil {
		return config{}, argErrorf("-ports: %v", err)
	}
	switch {
	case *intervalMs < minIntervalMs:
		return config{}, argErrorf("-interval must be at least %d ms", minIntervalMs)
	case *timeoutMs <= 0:
		return config{}, argErrorf("-timeout must be positive")
	case *logPath == "":
		return config{}, argErrorf("-log is required")
	case *count < 0:
		return config{}, argErrorf("-count must not be negative")
	}

	return config{
		targets:  targets,
		ports:    ports,
		interval: time.Duration(*intervalMs) * time.Millisecond,
		timeout:  time.Duration(*timeoutMs) * time.Millisecond,
		logPath:  *logPath,
		count:    *count,
		quiet:    *quiet,
	}, nil
}

func main() {
	cfg, err := parseArgs(os.Args[1:], os.Stderr)
	var ae *argError
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.As(err, &ae):
		fatalf("%v", err)
	default:
		os.Exit(2) // already reported by the flag package
	}

	f, err := os.OpenFile(cfg.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fatalf("open log: %v", err)
	}
	defer f.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	results := make(chan Result, 64)
	writerDone := make(chan struct{})
	go writeResults(f, results, !cfg.quiet, writerDone)

	run(ctx, cfg.targets, cfg.ports, cfg.interval, cfg.timeout, cfg.count, results)

	close(results)
	<-writerDone
}

// target is one probed address; host is what was given on the command line.
type target struct{ host, ip string }

// run is the central timing loop. On every tick it starts one probe for every
// host×port pair. It returns once the loop has stopped and every in-flight
// probe has reported its result.
func run(ctx context.Context, targets []target, ports []int, interval, timeout time.Duration, count int, results chan<- Result) {
	var (
		wg  sync.WaitGroup
		seq uint64
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for round := 0; count == 0 || round < count; round++ {
		if round > 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case <-ticker.C:
			}
		}
		for _, t := range targets {
			for _, p := range ports {
				seq++
				wg.Add(1)
				go func(seq uint64, t target, port int) {
					defer wg.Done()
					results <- probe(seq, t, port, timeout)
				}(seq, t, p)
			}
		}
	}
	wg.Wait()
}

func probe(seq uint64, t target, port int, timeout time.Duration) Result {
	addr := net.JoinHostPort(t.ip, strconv.Itoa(port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	end := time.Now()
	if conn != nil {
		conn.Close()
	}

	r := Result{
		Seq:       seq,
		Host:      t.host,
		IP:        t.ip,
		Port:      port,
		Start:     start.Format(timeLayout),
		StartUs:   start.UnixMicro(),
		End:       end.Format(timeLayout),
		ElapsedMs: float64(end.Sub(start).Microseconds()) / 1000,
	}
	switch {
	case err == nil:
		r.Outcome = "success"
	case errors.Is(err, syscall.ECONNREFUSED):
		r.Outcome = "closed"
	case isTimeout(err):
		r.Outcome = "timeout"
	default:
		r.Outcome = "error"
		r.Error = err.Error()
	}
	return r
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// writeResults is the only writer of the log file, so lines never interleave.
func writeResults(w io.Writer, results <-chan Result, echo bool, done chan<- struct{}) {
	defer close(done)
	enc := json.NewEncoder(w)
	for r := range results {
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(os.Stderr, "write log: %v\n", err)
		}
		if echo {
			fmt.Printf("%s %-21s %-7s %8.3f ms\n", r.Start, net.JoinHostPort(r.IP, strconv.Itoa(r.Port)), r.Outcome, r.ElapsedMs)
		}
	}
}

// parseHosts accepts IP literals (IPv6 zones like fe80::1%eth0 included).
// A hostname also works: it is resolved once here, so DNS time never counts
// towards connect time, and its first address is used.
func parseHosts(s string) ([]target, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("at least one host is required")
	}
	var targets []target
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		h := strings.TrimSpace(part)
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if h == "" {
			return nil, fmt.Errorf("empty entry in %q", s)
		}
		var ip string
		if addr, err := netip.ParseAddr(h); err == nil {
			ip = addr.Unmap().String()
		} else {
			addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), h)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %v", h, err)
			}
			ip = addrs[0].String()
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		targets = append(targets, target{host: h, ip: ip})
	}
	if len(targets) > maxHosts {
		return nil, fmt.Errorf("at most %d hosts allowed, got %d", maxHosts, len(targets))
	}
	return targets, nil
}

func parsePorts(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("at least one port is required")
	}
	var ports []int
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		p, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "connprobe: "+format+"\n", args...)
	os.Exit(2)
}
