package migrations

import (
	"context"

	ormstore "migration-orm"
	"orm/schema"
)

func init() {
	register(ormstore.Migration{
		Name: "2026_01_01_000000_create_users_table",
		Up: func(ctx context.Context, db *ormstore.DB) error {
			return db.Schema.Create(ctx, "users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name", 100)
				t.String("email", 254).Unique()
				t.String("password")
				t.Timestamps()
			})
		},
		Down: func(ctx context.Context, db *ormstore.DB) error {
			return db.Schema.DropIfExists(ctx, "users")
		},
	})
}
