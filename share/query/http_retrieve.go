package query

import (
	"net/http"

	errors2 "github.com/cloudradar-monitoring/rport/server/api/errors"
)

type RetrieveOptions struct {
	Fields []FieldsOption
}

func GetRetrieveOptions(req *http.Request) *RetrieveOptions {
	return &RetrieveOptions{
		Fields: ExtractFieldsOptions(req),
	}
}

func ValidateRetrieveOptions(lo *RetrieveOptions, supportedFields map[string]map[string]bool) error {
	errs := errors2.APIErrors{}

	fieldErrs := ValidateFieldsOptions(lo.Fields, supportedFields)
	if fieldErrs != nil {
		errs = append(errs, fieldErrs...)
	}

	if len(errs) > 0 {
		return errs
	}

	return nil
}
