package store

import (
	"errors"
	"strings"
)

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed: UNIQUE") {
		return errors.Join(ErrConflict, err)
	}
	return err
}
