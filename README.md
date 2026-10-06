# connprobe

A small, dependency-free command-line tool that repeatedly opens TCP connections
to a set of addresses and ports on a fixed schedule, and logs the outcome of
every attempt — with microsecond timestamps — as one JSON object per line.

It is meant for long-running reachability and latency observation: watching a
service across a failover, catching short outages that a monitoring system
with a one-minute resolution never sees, or comparing how IPv4 and IPv6 paths
to the same host behave over time.

```text
$ connprobe -hosts 192.168.0.4,2001:db8::10 -ports 22,443 -interval 400 -timeout 1500 -log probe.log
     1 2026-10-06T21:56:39.683518+02:00 192.168.0.4:22        success        0.406 ms
     1 2026-10-06T21:56:39.683579+02:00 192.168.0.4:443       closed         0.142 ms
     1 2026-10-06T21:56:39.683633+02:00 [2001:db8::10]:22     success        1.314 ms
     1 2026-10-06T21:56:39.683701+02:00 [2001:db8::10]:443    timeout     1500.211 ms
     2 2026-10-06T21:56:40.083522+02:00 192.168.0.4:22        success        0.398 ms
...
```

## Features

- **Multiple targets** — up to 10 IP addresses (IPv4 and IPv6 mixed) and any
  number of ports. Every round probes every address × port pair.
- **One central clock** — a single ticker starts every round, so all targets
  share exactly the same schedule. Probes run concurrently; a slow or hanging
  target never delays the others or the next round.
- **Clear outcomes** — `success` (with the connect time), `closed`
  (connection refused), `timeout`, `unreachable` (no route / ICMP
  unreachable) and `error` (anything else), with the system error message
  wherever it adds information.
- **Microsecond timestamps for both ends of every attempt** — when the
  connect was issued and when its outcome became known, each as wall-clock
  time (RFC 3339, local time zone) and as Unix epoch microseconds, so the log
  is easy to read *and* easy to process.
- **Log in start order, grouped by round** — every line carries its round
  (cycle) number; rounds are written in order, and within a round lines are
  ordered by their start time, no matter in which order the attempts finish.
- **Accurate durations** — elapsed time is measured on the monotonic clock and
  covers only the TCP connect; no DNS lookup happens inside a probe.
- **Safe, append-only JSON Lines log** — a single writer owns the file, lines
  never interleave, and an existing log is appended to, never truncated.
- **Clean shutdown** — on `Ctrl+C` / `SIGTERM` no new round is started, but
  every probe already in flight is allowed to finish and is logged.
- **No dependencies** — standard library only, a single static binary.

## Installation

Requires Go 1.21 or newer.

```bash
go install github.com/Savalinn/connprobe@latest
```

or from a checkout:

```bash
git clone https://github.com/Savalinn/connprobe.git
cd connprobe
go build -o connprobe .
```

For a fully static binary (e.g. to copy onto a minimal host):

```bash
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o connprobe .
```

## Usage

```text
connprobe -hosts <ip[,ip...]> -ports <port[,port...]> -log <path> [options]
```

| Flag        | Default | Description |
|-------------|---------|-------------|
| `-hosts`    | —       | **Required.** Comma-separated target addresses, at most 10. IPv4, IPv6 (`2001:db8::1` or `[2001:db8::1]`) and IPv6 link-local with zone (`fe80::1%eth0`) are accepted. Duplicates are ignored. |
| `-ports`    | —       | **Required.** Comma-separated TCP ports (1–65535). Duplicates are ignored. |
| `-log`      | —       | **Required.** Path of the JSON Lines log file. Created if missing, appended to if it exists. |
| `-interval` | `1000`  | Milliseconds between the start of two rounds. Minimum `400`. |
| `-timeout`  | `1000`  | Connect timeout in milliseconds. May be longer than `-interval`; probes then simply overlap. |
| `-count`    | `0`     | Number of rounds to run. `0` runs until interrupted. |
| `-quiet`    | `false` | Do not echo results to standard output; only write the log. |

Exit status is `0` after a normal run, an interrupt or `-h`, and `2` for
invalid arguments or when the log file cannot be opened.

### Examples

Watch an SSH and HTTPS endpoint over both address families, 2.5 times per second:

```bash
connprobe -hosts 192.168.0.4,2001:db8::10,2001:db8::11 -ports 22,443 \
             -interval 400 -timeout 1500 -log /var/tmp/probe.log
```

Probe a neighbour over IPv6 link-local on a specific interface:

```bash
connprobe -hosts fe80::1%eth0 -ports 22 -log ll.log
```

Run a fixed 10-minute sample in the background without terminal output:

```bash
nohup connprobe -hosts 10.0.0.5 -ports 5432 -interval 500 -count 1200 -quiet -log db.log &
```

## Log format

Every probe produces exactly one line ([JSON Lines](https://jsonlines.org/))
once it has finished, and lines appear in **start order** (see
[Ordering](#ordering)):

```json
{"round":7,"host":"192.168.0.4","ip":"192.168.0.4","port":443,"start":"2026-10-06T21:56:39.683579+02:00","start_us":1791316599683579,"end":"2026-10-06T21:56:39.683721+02:00","end_us":1791316599683721,"outcome":"closed","elapsed_ms":0.142}
{"round":7,"host":"2001:db8::10","ip":"2001:db8::10","port":22,"start":"2026-10-06T21:56:39.683633+02:00","start_us":1791316599683633,"end":"2026-10-06T21:56:39.684947+02:00","end_us":1791316599684947,"outcome":"success","elapsed_ms":1.314}
{"round":7,"host":"2001:db8::10","ip":"2001:db8::10","port":443,"start":"2026-10-06T21:56:39.683701+02:00","start_us":1791316599683701,"end":"2026-10-06T21:56:41.183912+02:00","end_us":1791316601183912,"outcome":"timeout","elapsed_ms":1500.211}
{"round":7,"host":"2001:db8::99","ip":"2001:db8::99","port":22,"start":"2026-10-06T21:56:39.683760+02:00","start_us":1791316599683760,"end":"2026-10-06T21:56:39.683783+02:00","end_us":1791316599683783,"outcome":"unreachable","elapsed_ms":0.023,"error":"dial tcp [2001:db8::99]:22: connect: network is unreachable"}
```

| Field        | Type   | Meaning |
|--------------|--------|---------|
| `round`      | int    | The round (cycle) the attempt belongs to, starting at 1. Round, `ip` and `port` together identify an attempt within a run. |
| `host`       | string | The target exactly as given on the command line. |
| `ip`         | string | The address actually connected to. |
| `port`       | int    | Target port. |
| `start`      | string | When the attempt was started: taken right before the `connect(2)` system call — RFC 3339, microsecond precision, local time zone. |
| `start_us`   | int    | The same instant as Unix epoch microseconds. |
| `end`        | string | When the outcome (`success`, `closed`, `timeout` or `error`) became known — same format as `start`. |
| `end_us`     | int    | The same instant as Unix epoch microseconds. |
| `outcome`    | string | `success`, `closed`, `timeout` or `error`. |
| `elapsed_ms` | float  | `end` − `start` in milliseconds with microsecond resolution, measured on the monotonic clock. |
| `error`      | string | For `unreachable` and `error`: the operating-system error message. |

### Outcomes

| Outcome       | Meaning |
|---------------|---------|
| `success` | The TCP handshake completed. `elapsed_ms` is the connect time. The connection is closed immediately afterwards. |
| `closed`  | The target actively refused the connection (TCP RST — usually "nothing listens on that port"). |
| `timeout` | No answer within `-timeout`. Typical for filtered ports, dropped packets or a host that is down. |
| `unreachable` | The target cannot be reached: there is no route to it locally (`network is unreachable`), or a router or firewall answered with ICMP destination unreachable — including *administratively prohibited* (`no route to host`). The message tells the cases apart. |
| `error`   | Any other failure, e.g. no usable source address for the address family, a local firewall rule (`operation not permitted`), or the open file limit. See `error`. |

### Ordering

Probes finish in any order — a refused connection is answered in
microseconds, a timeout takes the full `-timeout` — but the log is always in
**start order**. Finished results are collected per round in a log buffer:

- a round is written only when **every** attempt of that round has an
  outcome, and only after **every earlier round** has been written;
- within the round, lines are sorted by their measured `start_us`.

So `round` never decreases from one line to the next, and neither does
`start_us`. The price is latency, not data: a line can appear in the log up
to `-timeout` after its own attempt finished, because its round waits for
its slowest attempt, and a round waits for the rounds before it. The buffer
never holds more than the rounds currently in flight.

### Working with the log

```bash
# Outcome changes only: one line per target whenever its state flips
jq -c --slurp 'reduce .[] as $r ({last:{}, out:[]};
         ($r.ip + ":" + ($r.port|tostring)) as $k
         | if .last[$k] != $r.outcome then .out += [$r] | .last[$k] = $r.outcome else . end)
       | .out[]' probe.log

# Everything that was not a success
jq -c 'select(.outcome != "success")' probe.log

# Per target: attempts, failures and average/max connect time
jq -s 'group_by(.ip + ":" + (.port|tostring)) | map({
         target: (.[0].ip + ":" + (.[0].port|tostring)),
         attempts: length,
         failed: map(select(.outcome != "success")) | length,
         avg_ms: (map(select(.outcome == "success").elapsed_ms) | if length > 0 then add/length else null end),
         max_ms: (map(select(.outcome == "success").elapsed_ms) | max)
       })' probe.log
```

## Tip: record a packet capture alongside

> [!TIP]
> For any serious investigation, **start a packet capture next to connprobe**.
> The JSON log tells you *when* something went wrong and *what* the socket API
> saw; a capture tells you *why* — and the microsecond timestamps make it easy
> to jump from a log line straight to the matching packets.

connprobe only sees what the kernel reports back: success, refused, timed out
or an error code. Many of the interesting details never reach that layer:

- whether a `timeout` was a lost SYN, a lost SYN-ACK, or a SYN that was never
  answered at all;
- whether a slow `success` included SYN retransmissions (see below);
- who sent the reset behind a `closed` — the target host or a firewall in
  between (compare TTL / hop limit of the RST with a normal reply);
- which ICMP message produced an `error`, and from which router;
- TCP options, window sizes, MSS / path MTU and other handshake details.

A capture filtered to the probed targets stays small even over long runs.
Rotate it so it can run for days:

```bash
# Only the probed hosts and ports; ring of 20 x 100 MB files (probe.pcap00..19)
sudo tcpdump -i any -n -s 128 -w probe.pcap -C 100 -W 20 \
     '(host 192.168.0.4 or host 2001:db8::10) and (port 22 or port 443)'
```

`-s 128` keeps only the headers, which is all a connect probe needs. Note that
tcpdump drops root privileges after opening the interface and writes the files
as the unprivileged `tcpdump` user, so the output directory must be writable
by that user (or add `-Z root`). Run the
capture on the prober host; capturing on the target side as well (or on a
mirror port) shows whether packets were lost on the way there or on the way
back. To find a log entry in Wireshark, filter on its time window, e.g.
`frame.time_epoch >= 1791316599.683 && frame.time_epoch <= 1791316601.185`
(`start_us` / 1 000 000 gives the epoch seconds).

### Reading connect times: SYN retransmissions

On Linux an unanswered SYN is retransmitted after about **1 s**, then after a
further 2 s, 4 s, … (cumulative ≈ 1 s, 3 s, 7 s). This has two consequences:

- With `-timeout` of 1000 ms or less only a single SYN is ever sent, so **one
  lost packet already shows up as `timeout`**.
- With a longer timeout, a `success` with an `elapsed_ms` just above ~1000 (or
  ~3000) almost always means **the first SYN or SYN-ACK was lost** and the
  retransmission got through — packet loss, not a slow server. A capture
  confirms it immediately.

A `-timeout` of about 1500 ms is a good default for loss detection: a single
loss becomes a clearly recognisable ~1 s connect, two consecutive losses a
`timeout`.

## How it works

```text
            ┌──────────── ticker (interval) ────────────┐
            │                                            │
   round n: ├─ probe(ip1:p1) ─┐                          ├─ round n+1 ...
            ├─ probe(ip1:p2) ─┤   concurrent goroutines  │
            ├─ probe(ip2:p1) ─┤                          │
            └─ probe(ip2:p2) ─┘                          │
                     │  (finish in any order)
                     ▼
   results channel ──► log buffer (per round) ──► round complete and all earlier
                                                  rounds written? ──► sort by start,
                                                  JSON lines to log (+ stdout)
```

1. Arguments are validated and every target is turned into a concrete IP once,
   at startup.
2. The first round starts immediately; after that a single `time.Ticker`
   triggers each round.
3. Each round launches one goroutine per address × port pair, tagged with the
   round number. The start timestamp is taken in a `net.Dialer` `Control`
   hook, which runs after the socket has been created and immediately before
   `connect(2)`; the end timestamp right after the dial returns.
4. Results are sent to a channel drained by one writer goroutine, which owns
   the log file and writes complete rounds in order through the log buffer
   (see [Ordering](#ordering)).
5. On interrupt the loop stops, waits for every in-flight probe (at most
   `-timeout`), flushes the remaining results and exits.

## Limitations

These are known, deliberate trade-offs or edges worth knowing before relying
on the data.

- **It is a full TCP connect, not a raw SYN probe.** The tool uses the normal
  socket API: it completes the three-way handshake and then
  closes the connection. This needs no root privileges and gives reliable
  outcomes, but the target *does* see an established connection (which may
  show up in its logs, connection counters or rate limiters). A half-open,
  SYN-only probe would need raw sockets and elevated privileges and is not
  implemented.
- **Firewall behaviour shapes the outcome.** A firewall that *drops* packets
  yields `timeout`; one that *rejects with TCP reset* yields `closed` and is
  indistinguishable from a closed port; one that rejects with ICMP yields
  `error` (e.g. `no route to host`). `closed` therefore means "something sent a
  reset", not necessarily "the host is up and the port is closed".
- **Interface selection is limited to IPv6 link-local zones.** The `%iface`
  suffix is only meaningful for link-local addresses (`fe80::/10`). On a global
  IPv6 address it is accepted but ignored by the kernel — the route table
  decides the egress interface, while the log still shows the suffix. On IPv4
  it is not supported at all (the entry is rejected at startup). There is no
  option to bind a probe to a source address or interface.
- **Hostnames are resolved once.** Targets are expected to be IP addresses. A
  hostname is accepted, but it is resolved only at startup and only its first
  address is used; later DNS changes are not followed.
- **Missed rounds are skipped, not caught up.** If the process is paused (e.g.
  `SIGSTOP`, VM suspend, heavy CPU starvation), Go's ticker drops the ticks it
  could not deliver. The schedule then continues, without any record of the
  rounds that never ran. Round numbers stay consecutive, so look for gaps in
  `start_us` (consecutive rounds should start about `-interval` apart).
- **Timestamp resolution is not timestamp accuracy.** Times are logged with
  microsecond resolution. `start` is taken immediately before `connect(2)`, so
  it is very close to the SYN leaving the host. `end` is taken when the Go
  runtime hands the result back, which adds the network poller's wake-up
  latency (on an idle machine typically a few to tens of microseconds).
  A packet capture gives the exact wire times. `start` and `end` are
  wall-clock times and follow NTP adjustments; `elapsed_ms` uses the monotonic
  clock and does not. If the wall clock is stepped backwards, `start_us` can
  briefly decrease between rounds; the `round` number still reflects the true
  order.
- **No limit on concurrency.** With `-timeout` longer than `-interval` probes
  overlap; up to *hosts × ports × ⌈timeout / interval⌉* connection attempts can
  be in flight at once. A very large port list can therefore hit the open file
  limit (`ulimit -n`). Each successful probe also leaves a socket in
  `TIME_WAIT` on the prober side for about a minute, which is harmless at the
  supported rates but visible in `ss`.
- **No log rotation.** The log is opened once and appended to; it is not
  reopened on `SIGHUP`. Use `copytruncate`-style rotation, or restart the tool.
  Writes are not `fsync`'ed — the last lines may be lost on a power failure.
- **Finished results are held back in memory.** Because of the start-order
  guarantee a result can wait up to `-timeout` in the log buffer. A normal
  shutdown (`Ctrl+C`, `SIGTERM`, `-count`) always writes everything, but a
  hard kill (`SIGKILL`, crash, power loss) loses the results still waiting
  there — at most those of the last `-timeout` period.
- **Tested on Linux only.** Development and testing were done on Linux
  (including WSL2). It builds for macOS and Windows, but outcome
  classification relies on the platform's error codes — on Windows in
  particular, refused connections may be reported as `error` instead of
  `closed`.

## Development

```bash
go vet ./...
go test ./...
```

The tests cover command-line parsing and validation, the outcome
classification (`success`, `closed`, `timeout`, `unreachable`, `error`), the
JSON log format and timestamps, the log buffer (rounds written complete and in
order, lines sorted by start time), that every round probes every address ×
port pair exactly once, and that an interrupt stops the loop after the in-
flight round. The IPv6 part of the round test uses `::1`, so it needs IPv6
enabled on the loopback interface.

## License

[MIT](LICENSE.md) © 2026 Gabor Puskas
