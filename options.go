package funchose

import (
	"fmt"
	"log/slog"
	"time"
)

// Option configures a Provider.
type Option func(*options)

type options struct {
	table          string
	build          int64
	local          bool
	allowDowngrade bool
	allowDrop      bool
	overwriteDrift bool
	replicas       bool
	retireLimit    int
	removedSchemas []string
	failOn         []Action
	lockTimeout    time.Duration
	logger         *slog.Logger
}

const (
	defaultRetireLimit = 10
	defaultLockTimeout = 5 * time.Minute
)

func defaultOptions() options {
	return options{retireLimit: defaultRetireLimit, lockTimeout: defaultLockTimeout, logger: slog.Default()}
}

func (o options) validate() error {
	switch {
	case o.local && o.build != 0:
		return fmt.Errorf("%w: WithLocal and WithBuild(%d) exclude each other", ErrInvalidOption, o.build)
	case o.build < 0:
		return fmt.Errorf("%w: build %d: builds are positive numbers", ErrInvalidOption, o.build)
	}

	for _, action := range o.failOn {
		if !action.Warns() {
			return fmt.Errorf("%w: fail-on takes %s, %s, %s or %s, not %q",
				ErrInvalidOption, ActionDrift, ActionStale, ActionBlocked, ActionKept, action)
		}
	}

	return nil
}

// WithTable sets the history table, schema-qualified: billing.funchose_history.
// It is created on the first Up. When every file lives in one schema, the
// default is <schema>.funchose_history; with several schemas the table must be
// set. Each service needs a table of its own: a run treats every routine the
// table tracks as its own.
func WithTable(name string) Option { return func(o *options) { o.table = name } }

// WithBuild sets the number of the build being deployed, for example
// CI_PIPELINE_IID. Builds must grow over time. A build that is not newer than
// every build the history has seen for a routine, and is not the build that
// applied its current version, is stale: an old release, or one that was rolled
// back. Its files are not applied unless WithAllowDowngrade is set. Up and Plan
// need a build, or WithLocal.
func WithBuild(n int64) Option { return func(o *options) { o.build = n } }

// WithLocal runs as build 0, for a developer database. Build 0 is older than
// any real build, so a local run against a shared database never overwrites
// what CI applied. It exists so that a missing build number in CI fails instead
// of silently becoming 0.
func WithLocal() Option { return func(o *options) { o.local = true } }

// WithAllowDowngrade lets an older build put back its versions of routines, and
// marks the history records of newer builds as rolled back. Turn it on for a
// deliberate deploy of an older release. Leave it off when funchose runs at
// application start: a pod of the previous release restarting during a rollout
// would otherwise roll the routines back.
func WithAllowDowngrade(allow bool) Option { return func(o *options) { o.allowDowngrade = allow } }

// WithAllowDrop lets funchose drop routines it cannot prove unused: routines
// that a rollback goes back past (a newer build created them), and retired
// routines whose calls are not counted. Without it such routines stay and are
// reported as ActionBlocked. Routines that other objects depend on are never
// dropped.
func WithAllowDrop(allow bool) Option { return func(o *options) { o.allowDrop = allow } }

// WithOverwriteDrift lets funchose replace a definition it did not apply: one
// changed in the database by hand, or one a file changes on adoption. Without it
// an unchanged file leaves such a routine alone (ActionDrift), and a file that
// would change it fails the run with ErrDrift.
func WithOverwriteDrift(allow bool) Option { return func(o *options) { o.overwriteDrift = allow } }

// WithReplicas tells funchose that routines are also called on read replicas.
// The call counter of the primary does not see those calls, so it cannot prove
// that a retired routine is unused: such routines are dropped only with
// WithAllowDrop. A counter that grew still keeps a routine.
func WithReplicas(replicas bool) Option { return func(o *options) { o.replicas = replicas } }

// WithRetireLimit bounds how many routines one run may retire. Default 10. A run
// over the limit fails with ErrRetireLimit and changes nothing: a lost directory
// or a bad merge looks exactly like many removed files. 0 or less: no limit.
func WithRetireLimit(n int) Option { return func(o *options) { o.retireLimit = n } }

// WithRemovedSchemas names schemas whose directory was removed on purpose: their
// routines are retired like removed files. Without it a schema the history
// tracks but the files do not cover fails the run with ErrSchemaWithoutDir.
func WithRemovedSchemas(schemas ...string) Option {
	return func(o *options) { o.removedSchemas = append(o.removedSchemas, schemas...) }
}

// WithFailOn makes Up and Plan fail with ErrFailOn, applying nothing, when a
// routine ends up with one of the actions: ActionDrift, ActionStale,
// ActionBlocked or ActionKept. A deploy job usually fails on ActionStale; code
// that runs at application start does not, because pods of the previous
// release are stale by design.
func WithFailOn(actions ...Action) Option {
	return func(o *options) { o.failOn = append(o.failOn, actions...) }
}

// WithLockTimeout bounds the wait for another funchose run on the same history
// table. Default 5 minutes; 0 waits forever.
func WithLockTimeout(d time.Duration) Option { return func(o *options) { o.lockTimeout = d } }

// WithLogger sets the logger for applied routines and warnings. nil disables
// logging. Default slog.Default().
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }
