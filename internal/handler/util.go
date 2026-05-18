package handler

import (
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func parseUintParam(value string) uint {
	n, _ := strconv.ParseUint(value, 10, 64)
	return uint(n)
}

func gormExpr(sql string, values ...interface{}) clause.Expr {
	return gorm.Expr(sql, values...)
}
