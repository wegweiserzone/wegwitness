# Security Policy

A witness holds a Wegweiser cluster's log, which carries TSIG secrets and the cookie secret
in the clear, and it listens on the cluster port. A flaw here is a flaw in the cluster.

Reports go the way they go for Wegweiser, and reach the same person:
[Wegweiser's SECURITY.md](https://github.com/wegweiserzone/wegweiser/blob/main/SECURITY.md)
says how. In short, **do not open a public issue**: use GitHub's private reporting on this
repository, or write to **security@wegweiser.zone**.
