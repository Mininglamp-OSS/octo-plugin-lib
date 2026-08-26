package pluginstore

import "errors"

var (
	ErrInvalidArgument = errors.New("pluginstore: invalid argument")
	ErrNotFound        = errors.New("pluginstore: not found")
	ErrAlreadyExists   = errors.New("pluginstore: already exists")
	ErrConflict        = errors.New("pluginstore: conflict")
	ErrIntegrity       = errors.New("pluginstore: integrity failure")
	ErrStorage         = errors.New("pluginstore: storage failure")
)

type ErrorCode string

const (
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeAlreadyExists   ErrorCode = "ALREADY_EXISTS"
	CodeConflict        ErrorCode = "CONFLICT"
	CodeIntegrity       ErrorCode = "INTEGRITY_FAILURE"
	CodeInternal        ErrorCode = "INTERNAL"
)

func Code(err error) ErrorCode {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return CodeInvalidArgument
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrAlreadyExists):
		return CodeAlreadyExists
	case errors.Is(err, ErrConflict):
		return CodeConflict
	case errors.Is(err, ErrIntegrity):
		return CodeIntegrity
	default:
		return CodeInternal
	}
}
