package migrations

import "gorm.io/gorm"

// hasTable reports whether a table exists in the current database.
func hasTable(db *gorm.DB, table string) bool {
	var exists bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = ?)`, table,
	).Scan(&exists).Error; err != nil {
		return false
	}
	return exists
}
