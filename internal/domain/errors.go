package domain

import "errors"

var (
	ErrInvalidInput  = errors.New("invalid input")
	ErrNotFound      = errors.New("not found")
	ErrInvalidWindow = errors.New("invalid time window")
)
