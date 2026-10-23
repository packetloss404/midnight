package audit

import "regexp"

var partitionLabelRegex = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
