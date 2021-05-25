package errors

type RetryableError struct {
	err error
}

// Error implements the Error interface.
func (e *RetryableError) Error() string {
	return e.err.Error()
}

func NewRetryableError(err error) error {
	return &RetryableError{err}
}

func IsRetryable(err error) bool {
	_, isRetryable := err.(*RetryableError)
	return isRetryable
}
