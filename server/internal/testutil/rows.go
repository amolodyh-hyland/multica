package testutil

import (
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// UUIDRows supplies a one-column metadata result to transaction-starter fakes.
// It deliberately rejects a scan shape other than the workspace-ID projection.
func UUIDRows(ids ...pgtype.UUID) pgx.Rows { return &uuidRows{ids: ids} }

type uuidRows struct {
	pgx.Rows
	ids     []pgtype.UUID
	current pgtype.UUID
}

func (r *uuidRows) Next() bool {
	if len(r.ids) == 0 {
		return false
	}
	r.current = r.ids[0]
	r.ids = r.ids[1:]
	return true
}
func (r *uuidRows) Close()     { r.ids = nil }
func (r *uuidRows) Err() error { return nil }
func (r *uuidRows) Scan(dest ...any) error {
	if len(dest) != 1 {
		return fmt.Errorf("UUIDRows: expected one column")
	}
	p, ok := dest[0].(*pgtype.UUID)
	if !ok {
		return fmt.Errorf("UUIDRows: expected UUID destination")
	}
	*p = r.current
	return nil
}
