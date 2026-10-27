package db

import "github.com/multica-ai/multica/server/pkg/db/bindingtx"

// NewBinding applies the transaction prefix to standalone writes and locking
// reads. Write transactions must begin with bindingtx.Begin before any SQL.
func NewBinding(db DBTX) *Queries {
	return New(bindingtx.Wrap(db))
}
