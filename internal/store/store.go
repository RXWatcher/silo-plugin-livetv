// Package store wraps pgx for the live TV plugin. Domain-specific
// wrappers live in sibling files; this file holds shared scaffolding.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a thin pgx wrapper for the live TV plugin.
type Store struct {
	Pool *pgxpool.Pool
}

// New constructs a Store bound to the given pool.
func New(p *pgxpool.Pool) *Store { return &Store{Pool: p} }

// ErrNotFound is returned by Get* methods when a row does not exist.
var ErrNotFound = errors.New("not found")
