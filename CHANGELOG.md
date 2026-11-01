# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until v1.0.0 the
behaviour may change without a deprecation period.

## [Unreleased]

### Added

- Every release carries deb and rpm packages for amd64 and arm64 beside the archives,
  listed in `checksums.txt`. They install `wegwitness` and its systemd unit. Installing
  starts nothing. The configuration file is written from the example only where there is
  none, and is never replaced afterwards. An upgrade restarts a witness that was running
  and leaves a stopped one stopped. Purging takes the configuration and leaves the log.

### Security

- The systemd unit reads `/etc/wegwitness/config.yaml` as a credential, so the file can be
  mode 0600 and owned by root, as one holding the cluster's secret should be. The witness
  runs as a dynamic user, and until now could read the file only if every account on the
  host could. It no longer starts without the file. A unit installed by hand has to be
  replaced to get this, and a `--join` added to it now belongs in a drop-in, as the unit's
  comment shows.

## [0.1.0] - 2026-10-06

The first release. It works with Wegweiser 0.5.0 or later, the first release whose
cluster takes a witness.

### Added

- `wegwitness serve` runs a witness for a Wegweiser cluster: a voter that keeps the log,
  applies none of it, and answers no queries. It mints an identifier beginning `witness-`
  on its first start, and joins with `--join` and the cluster address of any member, in
  the two steps a voter joins in. The flag is ignored once it is a member.

- A witness that wins an election hands leadership to a member that holds data, trying
  each in turn until one takes it.

- Asked over the cluster port how far it has got, a witness answers the way a member does,
  so `weg cluster status` lists it with its progress. Anything else sent to it is refused
  with 503.

- A log snapshot a witness writes says a witness wrote it, so that a member with a store
  refuses to restore it rather than empty itself. A witness keeps a hundred times Raft's
  default log behind its snapshots, so that a member is brought up to date from the log.

- `wegwitness health` asks the running witness, on its own cluster port and with the
  cluster's secret, whether it takes part in a cluster, and exits 0 when it does. A
  witness still waiting to be added, or one taken out, is not healthy: it holds no vote.

- `make interop` runs a witness against real `weg` servers.

- `packaging/Containerfile` builds an image of the binary on `scratch`, running as a
  user without privileges, with `wegwitness health` as its health check. `make image`
  builds it with Podman. The configuration is mounted rather than baked in, since a
  witness cannot start without the cluster's secret.

- A tag publishes a release: binaries for linux/amd64 and linux/arm64 with the licence,
  the example configuration and the systemd unit beside them, and the image at
  `ghcr.io/wegweiserzone/wegwitness`. Before anything is published, the witness is run
  against the oldest Wegweiser it claims to work with.

[Unreleased]: https://github.com/wegweiserzone/wegwitness/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/wegweiserzone/wegwitness/releases/tag/v0.1.0
