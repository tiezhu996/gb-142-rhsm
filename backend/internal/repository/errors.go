package repository

import "errors"

var ErrNotFound = errors.New("not found")

// ErrDuplicate 唯一键冲突，例如幂等键 message_id 已被占用。
var ErrDuplicate = errors.New("duplicate key")

// ErrConflict 幂等键已被不同负载（对象、渠道或说明）的请求占用。
var ErrConflict = errors.New("conflict")
