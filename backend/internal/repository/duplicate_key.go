package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// ErrDuplicateKey wraps a driver-level unique constraint violation.
var ErrDuplicateKey = errors.New("duplicate key")

// IsDuplicateKeyErr reports whether err is a unique constraint violation from
// either MySQL (production) or SQLite (tests).
func IsDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return true
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") || strings.Contains(msg, "unique constraint failed")
}
