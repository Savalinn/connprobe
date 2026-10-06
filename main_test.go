// Copyright (c) 2026 Gabor Puskas
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParsePorts(t *testing.T) {
	got, err := parsePorts(" 22,80 ,443,80")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{22, 80, 443}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	for _, bad := range []string{"", "0", "65536", "http", "22,,80"} {
		if _, err := parsePorts(bad); err == nil {
			t.Errorf("parsePorts(%q): expected error", bad)
		}
	}
}

func TestParseHosts(t *testing.T) {
	got, err := parseHosts("192.168.0.4, 2001:db8::1,[2001:db8::2],::ffff:192.168.0.4,fe80::1%lo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.0.4", "2001:db8::1", "2001:db8::2", "fe80::1%lo"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].ip != want[i] {
			t.Errorf("target %d: ip %q, want %q", i, got[i].ip, want[i])
		}
	}

	if _, err := parseHosts(""); err == nil {
		t.Error("empty list: expected error")
	}
	if _, err := parseHosts("10.0.0.1,"); err == nil {
		t.Error("trailing comma: expected error")
	}
	eleven := "10.0.0.1,10.0.0.2,10.0.0.3,10.0.0.4,10.0.0.5,10.0.0.6,10.0.0.7,10.0.0.8,10.0.0.9,10.0.0.10,10.0.0.11"
	if _, err := parseHosts(eleven); err == nil {
		t.Error("11 hosts: expected error")
	}
}

func TestProbeOutcomes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	open := ln.Addr().(*net.TCPAddr).Port

	// Grab a free port and release it, so nothing listens there.
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := tmp.Addr().(*net.TCPAddr).Port
	tmp.Close()

	local := target{host: "127.0.0.1", ip: "127.0.0.1"}
	if r := probe(1, local, open, time.Second); r.Outcome != "success" {
		t.Errorf("open port %d: outcome %q (%s)", open, r.Outcome, r.Error)
	}
	if r := probe(2, local, closed, time.Second); r.Outcome != "closed" {
		t.Errorf("closed port %d: outcome %q (%s)", closed, r.Outcome, r.Error)
	}
}

func TestRunProbesEveryPair(t *testing.T) {
	targets := []target{{"127.0.0.1", "127.0.0.1"}, {"::1", "::1"}}
	ports := []int{1, 2, 3}
	results := make(chan Result, 64)

	run(context.Background(), targets, ports, minIntervalMs*time.Millisecond, 200*time.Millisecond, 2, results)
	close(results)

	seen := map[string]int{}
	for r := range results {
		seen[net.JoinHostPort(r.IP, strconv.Itoa(r.Port))]++
	}
	if len(seen) != len(targets)*len(ports) {
		t.Fatalf("probed %d distinct pairs, want %d: %v", len(seen), len(targets)*len(ports), seen)
	}
	for pair, n := range seen {
		if n != 2 {
			t.Errorf("%s probed %d times, want 2", pair, n)
		}
	}
}

func TestParseArgs(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-hosts", "192.168.0.4,2001:db8::1", "-ports", "22,443",
		"-interval", "400", "-timeout", "1500", "-count", "3", "-quiet", "-log", "probe.log",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.targets) != 2 || len(cfg.ports) != 2 {
		t.Errorf("targets %v, ports %v", cfg.targets, cfg.ports)
	}
	if cfg.interval != 400*time.Millisecond || cfg.timeout != 1500*time.Millisecond {
		t.Errorf("interval %v, timeout %v", cfg.interval, cfg.timeout)
	}
	if cfg.count != 3 || !cfg.quiet || cfg.logPath != "probe.log" {
		t.Errorf("count %d, quiet %v, log %q", cfg.count, cfg.quiet, cfg.logPath)
	}

	defaults, err := parseArgs([]string{"-hosts", "::1", "-ports", "1", "-log", "x"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.interval != time.Second || defaults.timeout != time.Second || defaults.count != 0 || defaults.quiet {
		t.Errorf("unexpected defaults: %+v", defaults)
	}
}

func TestParseArgsRejects(t *testing.T) {
	valid := []string{"-hosts", "::1", "-ports", "1", "-log", "x"}
	cases := map[string][]string{
		"missing hosts":    {"-ports", "1", "-log", "x"},
		"missing ports":    {"-hosts", "::1", "-log", "x"},
		"missing log":      {"-hosts", "::1", "-ports", "1"},
		"interval too low": append([]string{"-interval", "399"}, valid...),
		"zero timeout":     append([]string{"-timeout", "0"}, valid...),
		"negative count":   append([]string{"-count", "-1"}, valid...),
		"extra argument":   append(append([]string{}, valid...), "extra"),
	}
	for name, args := range cases {
		_, err := parseArgs(args, io.Discard)
		var ae *argError
		if !errors.As(err, &ae) {
			t.Errorf("%s: got %v, want an argument error", name, err)
		}
	}

	if _, err := parseArgs([]string{"-bogus"}, io.Discard); err == nil || errors.As(err, new(*argError)) {
		t.Errorf("unknown flag: got %v, want a flag parse error", err)
	}
	if _, err := parseArgs([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: got %v, want flag.ErrHelp", err)
	}
}

func TestIsTimeout(t *testing.T) {
	deadline := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if !isTimeout(deadline) {
		t.Error("deadline exceeded not classified as timeout")
	}
	if isTimeout(errors.New("boom")) {
		t.Error("plain error classified as timeout")
	}
}

func TestWriteResults(t *testing.T) {
	var buf bytes.Buffer
	results := make(chan Result, 2)
	done := make(chan struct{})
	results <- Result{Seq: 1, Host: "::1", IP: "::1", Port: 22, Outcome: "success", ElapsedMs: 0.25}
	results <- Result{Seq: 2, Host: "::1", IP: "::1", Port: 23, Outcome: "error", Error: "boom"}
	close(results)
	writeResults(&buf, results, false, done)
	<-done

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i+1, err)
		}
		for _, key := range []string{"seq", "host", "ip", "port", "start", "start_us", "end", "outcome", "elapsed_ms"} {
			if _, ok := m[key]; !ok {
				t.Errorf("line %d: missing field %q", i+1, key)
			}
		}
		_, hasErr := m["error"]
		if hasErr != (m["outcome"] == "error") {
			t.Errorf("line %d: error field present=%v for outcome %v", i+1, hasErr, m["outcome"])
		}
	}
}

func TestProbeTimestamps(t *testing.T) {
	r := probe(1, target{"127.0.0.1", "127.0.0.1"}, 1, time.Second)
	start, err := time.Parse(time.RFC3339Nano, r.Start)
	if err != nil {
		t.Fatalf("start %q: %v", r.Start, err)
	}
	end, err := time.Parse(time.RFC3339Nano, r.End)
	if err != nil {
		t.Fatalf("end %q: %v", r.End, err)
	}
	if start.UnixMicro() != r.StartUs {
		t.Errorf("start %s does not match start_us %d", r.Start, r.StartUs)
	}
	if end.Before(start) || r.ElapsedMs < 0 {
		t.Errorf("end %s before start %s, elapsed %v ms", r.End, r.Start, r.ElapsedMs)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := make(chan Result, 16)

	finished := make(chan struct{})
	go func() {
		run(ctx, []target{{"127.0.0.1", "127.0.0.1"}}, []int{1, 2}, time.Hour, 200*time.Millisecond, 0, results)
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
	close(results)
	n := 0
	for range results {
		n++
	}
	// The first round always runs; cancellation stops the loop before the next.
	if n != 2 {
		t.Errorf("got %d results, want 2 (one full round)", n)
	}
}
