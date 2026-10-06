# wegwitness

A witness for a [Wegweiser](https://github.com/wegweiserzone/wegweiser) cluster: a member
that votes and keeps the cluster's log, and does nothing else. It holds no zones, answers no
queries and has no API.

Two DNS servers make no honest cluster. A majority of two is two, so either one going down
stops every change. A third server that holds a copy of everything fixes that and costs a
third copy of everything. A witness is the cheaper third vote: two servers answering DNS,
and one small machine holding the log.

What a witness is, and why it behaves as it does, is settled in Wegweiser's decision
records: [D39](https://github.com/wegweiserzone/wegweiser/blob/main/docs/decisions/d39-the-witness.md)
for the witness itself, [D48](https://github.com/wegweiserzone/wegweiser/blob/main/docs/decisions/d48-a-witness-is-known-by-its-identifier.md)
for how the cluster tells it apart. In short:

- **It is always a voter**, and witnesses stay fewer than half of the voters. One witness
  beside two servers is the shape it exists for; the cluster refuses a second.
- **It never leads.** When it wins an election it hands leadership straight to a server
  that holds data, and Raft brings that server up to date from the witness's log first. A
  server that was down while a change was made, and whose partner went down before it
  caught up, is repaired that way.
- **It is as sensitive as a server.** The log carries TSIG secrets and the cookie secret in
  the clear. A witness is not a low-trust box, whatever its size.

## Running one

Build it with `make build`, or `go install github.com/wegweiserzone/wegwitness/cmd/wegwitness@latest`.

Write a configuration file with the cluster's secret and the address the members reach the
witness at; [docs/wegwitness.example.yaml](docs/wegwitness.example.yaml) describes every
setting. Then start it once with the cluster address of any member:

```console
$ wegwitness serve --config /etc/wegwitness/config.yaml --join 192.0.2.1:8054
```

It asks to be added, waits for the log to reach it, and is a voter from then on. The flag
does nothing on later starts, so a unit file can keep it. A sandboxed systemd unit is in
[packaging/systemd](packaging/systemd/wegwitness.service).

`wegwitness health` asks the running witness whether it takes part in a cluster, and exits
0 when it does, for a monitoring system or a container runtime to call.

`weg cluster status`, on any member, lists the witness with the role `witness` and how far
it has got. To take it out, remove it from a member: `weg cluster remove witness-…`.

## The transport is written twice

A witness speaks Wegweiser's cluster transport: TLS 1.3, the shared secret proved inside
the session, and one octet saying what a stream carries. Everything in Wegweiser is under
`internal/`, so the transport here is a copy rather than an import, by decision rather than
by accident. [internal/transport](internal/transport/transport.go) says which file it
copies. The two have to agree on every constant, and `make interop` runs a witness against
real `weg` servers to show that they do:

```console
$ make interop WEG_BIN=../wegweiser/bin/weg
```

## Building

Needs the Go version in [go.mod](go.mod), or newer. No cgo.

```console
$ make build      # bin/wegwitness
$ make check      # the gate: tidy, format, vet, lint, tests
$ make help       # all targets
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Commits carry a DCO sign-off.

## License

[AGPL-3.0-or-later](LICENSE), as Wegweiser is.
