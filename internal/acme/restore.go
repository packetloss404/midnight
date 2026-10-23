package acme

import (
	"errors"
	"time"
)

type RestoreSlot struct {
	Index     int
	NotBefore time.Time
}

func PlanRestoreThrottle(certCount int, start time.Time) ([]RestoreSlot, error) {
	if certCount < 0 {
		return nil, errors.New("certificate count must be greater than or equal to zero")
	}
	slots := make([]RestoreSlot, certCount)
	for i := range slots {
		slots[i] = RestoreSlot{
			Index:     i,
			NotBefore: start.Add(time.Duration(i/RestoreCertificatesPerHour) * time.Hour),
		}
	}
	return slots, nil
}
