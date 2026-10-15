package funchose

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
)

// Provider applies the routine files of one file system to one database.
type Provider struct {
	db       *sql.DB
	routines []Routine
	schemas  []string // schemas the files cover, in file order
	table    string   // history table, schema-qualified
	opts     options
}

// NewProvider loads and checks the routine files of fsys: <schema>/<name>.sql
// at its root. Use fs.Sub to point it at a subdirectory of an embed.FS.
func NewProvider(db *sql.DB, fsys fs.FS, opts ...Option) (*Provider, error) {
	cfg := defaultOptions()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	routines, problems, err := Load(fsys)
	if err != nil {
		return nil, fmt.Errorf("funchose: read routines: %w", err)
	}

	if len(problems) > 0 {
		msgs := make([]string, len(problems))
		for i, p := range problems {
			msgs[i] = p.String()
		}

		return nil, fmt.Errorf("%w:\n  %s", ErrInvalidFiles, strings.Join(msgs, "\n  "))
	}

	seen := map[string]bool{}

	var schemas []string

	for _, r := range routines {
		if !seen[r.Schema] {
			seen[r.Schema] = true
			schemas = append(schemas, r.Schema)
		}
	}

	table, err := historyTable(cfg.table, schemas)
	if err != nil {
		return nil, err
	}

	return &Provider{db: db, routines: routines, schemas: schemas, table: table, opts: cfg}, nil
}

// Table returns the schema-qualified history table the provider uses.
func (p *Provider) Table() string { return p.table }

// Up applies every changed routine in one transaction and returns what it did.
// On error nothing is applied.
//
// Up first compares files, history and database without the lock, the way
// Status does. Most runs, pod restarts above all, find nothing to do and return
// right there, so they never queue behind each other. Only a run with work
// takes the lock and checks again under it.
func (p *Provider) Up(ctx context.Context) (Report, error) {
	if err := p.requireBuild(); err != nil {
		return p.report(), err
	}

	rep, work, err := p.run(ctx, modeCheck)
	if err != nil {
		return rep, err
	}

	if work {
		rep, _, err = p.run(ctx, modeUp)

		return rep, err
	}

	if err := p.failOn(rep); err != nil {
		return rep, err
	}

	rep.log(p.opts.logger)

	return rep, nil
}

// Plan does everything Up does, including the CREATE OR REPLACE statements and
// their checks, then rolls the transaction back. Changes carry a diff of the
// database definitions. Plan takes the lock and needs the same rights as Up.
func (p *Provider) Plan(ctx context.Context) (Report, error) {
	if err := p.requireBuild(); err != nil {
		return p.report(), err
	}

	rep, _, err := p.run(ctx, modePlan)

	return rep, err
}

// Status compares files, history and database without writing anything and
// without taking the lock. It cannot tell whether adopting a routine would
// change it: Plan can. WithFailOn does not apply to it.
func (p *Provider) Status(ctx context.Context) (Report, error) {
	rep, _, err := p.run(ctx, modeStatus)

	return rep, err
}

func (p *Provider) report() Report { return Report{Build: p.opts.build, Table: p.table} }

func (p *Provider) requireBuild() error {
	if p.opts.local || p.opts.build > 0 {
		return nil
	}

	return fmt.Errorf("%w: pass the number of the build being deployed (WithBuild, -build $CI_PIPELINE_IID), "+
		"or WithLocal (-local) for a developer database", ErrBuildRequired)
}

// failOn turns the actions WithFailOn names into an error.
func (p *Provider) failOn(rep Report) error {
	hits := rep.With(p.opts.failOn...)
	if len(hits) == 0 {
		return nil
	}

	names := make([]string, len(hits))
	for i, c := range hits {
		names[i] = string(c.Action) + " " + c.Routine()
	}

	return fmt.Errorf("%w: %s", ErrFailOn, strings.Join(names, ", "))
}
