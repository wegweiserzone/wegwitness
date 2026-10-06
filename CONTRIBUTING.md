# Contributing to wegwitness

wegwitness is a small part of [Wegweiser](https://github.com/wegweiserzone/wegweiser), and
the way contributions work there is the way they work here:
[Wegweiser's CONTRIBUTING.md](https://github.com/wegweiserzone/wegweiser/blob/main/CONTRIBUTING.md)
covers the Developer Certificate of Origin, the commit messages and what a patch is
expected to bring. The short version:

- Every commit carries a `Signed-off-by:` line (`git commit -s`), which means you agree to
  the [Developer Certificate of Origin 1.1](https://developercertificate.org/). There is no
  CLA, and your contribution is licensed under AGPL-3.0-or-later.
- `make check` passes before a pull request is opened.
- A change to how a witness behaves starts as a decision record in Wegweiser's
  `docs/decisions/`, because that is where D39 and D48 live.

## The transport

[internal/transport](internal/transport/transport.go) is a copy of Wegweiser's
`internal/cluster/transport.go`. A change to the protocol is made there first and copied
here, and `make interop`, run against a `weg` built from the same change, is how both sides
are shown to still agree.
