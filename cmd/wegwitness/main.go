// Command wegwitness is a witness for a Wegweiser cluster: a voter that keeps
// the log, applies none of it, and answers no queries
// (wegweiser's docs/decisions/d39-the-witness.md).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wegweiserzone/wegwitness/internal/buildinfo"
	"github.com/wegweiserzone/wegwitness/internal/config"
	"github.com/wegweiserzone/wegwitness/internal/transport"
	"github.com/wegweiserzone/wegwitness/internal/witness"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `wegwitness keeps a Wegweiser cluster's log and votes, and answers nothing else.

Usage:
  wegwitness serve [--config FILE] [--join ADDRESS]
  wegwitness health [--config FILE]
  wegwitness version

A witness joins from its first start, with --join and the cluster address of
any member. It leaves when a member removes it: weg cluster remove ID.

health asks the running witness, on its own cluster port, whether it takes
part in a cluster, and exits 0 when it does.
`

func main() {
	// os.Exit is confined to this line so that run can use defer.
	os.Exit(run())
}

func run() int {
	// A first signal stops the witness cleanly; a second is left to the
	// default handler, which stops it at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

// Main runs one command and returns the exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "serve", "health":
		run := serve
		if args[0] == "health" {
			run = func(ctx context.Context, args []string, stderr io.Writer) error {
				return health(ctx, args, stdout, stderr)
			}
		}
		err := run(ctx, args[1:], stderr)
		switch {
		case errors.Is(err, flag.ErrHelp):
			return exitOK
		case errors.As(err, new(usageError)):
			fmt.Fprintf(stderr, "wegwitness: %v\n\n%s", err, usage)
			return exitUsage
		case err != nil:
			fmt.Fprintf(stderr, "wegwitness: %v\n", err)
			return exitError
		}
		return exitOK
	case "version":
		if err := json.NewEncoder(stdout).Encode(buildinfo.Get()); err != nil {
			return exitError
		}
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "wegwitness: %q is not a command\n\n%s", args[0], usage)
		return exitUsage
	}
}

// usageError is a command line that does not say what it means.
type usageError struct{ error }

func serve(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "/etc/wegwitness/config.yaml", "the configuration file")
	join := fs.String("join", "", "the cluster address of a member to ask to be added by, on the first start")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Errorf("serve takes no arguments, and was given %q", fs.Args())}
	}

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.Level}))

	id, err := witness.Identity(cfg.Dir, cfg.ID)
	if err != nil {
		return err
	}
	tr, err := transport.New(transport.Config{
		Secret: cfg.Secret,
		OnRefused: func(remote net.Addr, why error) {
			log.Debug("turned a connection away", "from", remote, "why", why)
		},
	})
	if err != nil {
		return err
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen for the other members: %w", err)
	}
	mux := tr.Serve(l)
	node, err := witness.Start(witness.Config{
		ID: id, Advertise: cfg.Advertise, Dir: cfg.Dir, Transport: tr, Mux: mux, Logger: log,
	})
	if err != nil {
		return errors.Join(err, mux.Close())
	}
	defer func() {
		if cerr := errors.Join(node.Close(), mux.Close()); cerr != nil {
			log.Warn("stop", "error", cerr)
		}
	}()

	switch {
	case *join != "" && node.Replicating():
		// A unit file that keeps the flag is harmless (D44).
		log.Info("this witness is a cluster member already; --join is ignored", "member", id)
	case *join != "":
		log.Info("asking to join the cluster", "member", id, "via", *join)
		if jerr := node.Join(ctx, *join); jerr != nil {
			return jerr
		}
	case !node.Replicating():
		log.Info("this witness is in no cluster yet; start it with --join and a member's cluster address",
			"member", id)
	}
	if node.Replicating() {
		log.Info("taking part in the cluster", "member", id, "advertise", cfg.Advertise, "dir", cfg.Dir)
	}
	<-ctx.Done()
	return nil
}

// healthWait bounds the whole question, so that a health check of a witness
// that hangs fails rather than hangs with it.
const healthWait = 5 * time.Second

func health(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "/etc/wegwitness/config.yaml", "the configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Errorf("health takes no arguments, and was given %q", fs.Args())}
	}

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	tr, err := transport.New(transport.Config{Secret: cfg.Secret})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, healthWait)
	defer cancel()
	p, err := witness.Probe(ctx, tr, local(cfg.Listen))
	if err != nil {
		return err
	}

	switch {
	case !p.Replicating:
		return fmt.Errorf("%s is in no cluster yet; start it with --join and a member's cluster address", p.ID)
	case p.Removed:
		return fmt.Errorf("%s has been taken out of its cluster", p.ID)
	}
	leader := p.Leader
	if leader == "" {
		leader = "nobody, for the moment"
	}
	_, err = fmt.Fprintf(stdout, "%s takes part: applied %d of %d, %s leads\n", p.ID, p.Applied, p.Committed, leader)
	return err
}

// local is the address this witness is asked at from the same machine: the
// one it listens on, with loopback for an address that means every
// interface. The address it advertises is the one the other members reach,
// which from inside a container often is not this machine at all.
func local(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
