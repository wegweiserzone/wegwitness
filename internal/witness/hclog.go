package witness

import (
	"context"
	"io"
	"log"
	"log/slog"
	"slices"

	"github.com/hashicorp/go-hclog"
)

// raftLogger hands Raft's log lines to the logger everything else reports
// through.
//
// Raft logs through hclog, whose interface is twenty methods wide. Implementing
// them over slog, rather than letting Raft keep a logger of its own, puts its
// lines where every other line is, at the level the operator chose, and leaves
// no second output to configure.
type raftLogger struct {
	log     *slog.Logger
	name    string
	implied []any
}

var _ hclog.Logger = (*raftLogger)(nil)

func newRaftLogger(l *slog.Logger) *raftLogger {
	return &raftLogger{log: l, name: "raft"}
}

// slogLevel is where an hclog level lands. Trace sits below debug, as it does
// in hclog. A line without a level is an ordinary one, and Off is not a level
// anything is written at.
func slogLevel(level hclog.Level) (slog.Level, bool) {
	switch level {
	case hclog.Trace:
		return slog.LevelDebug - 4, true
	case hclog.Debug:
		return slog.LevelDebug, true
	case hclog.Info, hclog.NoLevel:
		return slog.LevelInfo, true
	case hclog.Warn:
		return slog.LevelWarn, true
	case hclog.Error:
		return slog.LevelError, true
	default:
		return 0, false
	}
}

func (l *raftLogger) Log(level hclog.Level, msg string, args ...any) {
	lvl, ok := slogLevel(level)
	if !ok {
		return
	}
	l.log.Log(context.Background(), lvl, msg, l.attrs(args)...)
}

func (l *raftLogger) attrs(args []any) []any {
	out := make([]any, 0, 2+len(l.implied)+len(args))
	out = append(out, "component", l.name)
	out = append(out, l.implied...)
	return append(out, args...)
}

func (l *raftLogger) Trace(msg string, args ...any) { l.Log(hclog.Trace, msg, args...) }
func (l *raftLogger) Debug(msg string, args ...any) { l.Log(hclog.Debug, msg, args...) }
func (l *raftLogger) Info(msg string, args ...any)  { l.Log(hclog.Info, msg, args...) }
func (l *raftLogger) Warn(msg string, args ...any)  { l.Log(hclog.Warn, msg, args...) }
func (l *raftLogger) Error(msg string, args ...any) { l.Log(hclog.Error, msg, args...) }

func (l *raftLogger) enabled(level hclog.Level) bool {
	lvl, ok := slogLevel(level)
	return ok && l.log.Enabled(context.Background(), lvl)
}

func (l *raftLogger) IsTrace() bool { return l.enabled(hclog.Trace) }
func (l *raftLogger) IsDebug() bool { return l.enabled(hclog.Debug) }
func (l *raftLogger) IsInfo() bool  { return l.enabled(hclog.Info) }
func (l *raftLogger) IsWarn() bool  { return l.enabled(hclog.Warn) }
func (l *raftLogger) IsError() bool { return l.enabled(hclog.Error) }

func (l *raftLogger) ImpliedArgs() []any { return l.implied }

func (l *raftLogger) With(args ...any) hclog.Logger {
	return &raftLogger{log: l.log, name: l.name, implied: append(slices.Clip(l.implied), args...)}
}

func (l *raftLogger) Name() string { return l.name }

func (l *raftLogger) Named(name string) hclog.Logger {
	return &raftLogger{log: l.log, name: l.name + "." + name, implied: l.implied}
}

func (l *raftLogger) ResetNamed(name string) hclog.Logger {
	return &raftLogger{log: l.log, name: name, implied: l.implied}
}

// SetLevel does nothing. The level belongs to the handler the operator
// configured, and Raft does not get to raise or lower it.
func (l *raftLogger) SetLevel(hclog.Level) {}

// GetLevel reports the lowest level the handler writes.
func (l *raftLogger) GetLevel() hclog.Level {
	for _, level := range []hclog.Level{hclog.Trace, hclog.Debug, hclog.Info, hclog.Warn, hclog.Error} {
		if l.enabled(level) {
			return level
		}
	}
	return hclog.Off
}

func (l *raftLogger) StandardLogger(opts *hclog.StandardLoggerOptions) *log.Logger {
	level := slog.LevelInfo
	if opts != nil && opts.ForceLevel != hclog.NoLevel {
		if lvl, ok := slogLevel(opts.ForceLevel); ok {
			level = lvl
		}
	}
	return slog.NewLogLogger(l.log.With("component", l.name).Handler(), level)
}

func (l *raftLogger) StandardWriter(opts *hclog.StandardLoggerOptions) io.Writer {
	return l.StandardLogger(opts).Writer()
}
