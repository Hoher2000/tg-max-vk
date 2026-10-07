package domain

import "errors"

var (
	ErrNotFound         = errors.New("not found")
	ErrUnsupported      = errors.New("operation not supported")
	ErrAlreadyForwarded = errors.New("message already forwarded")
	ErrRateLimited      = errors.New("rate limited")
)