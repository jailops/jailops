package fleettest

import (
	"filippo.io/age"
)

func parseRecipient(s string) ([]age.Recipient, error) {
	r, err := age.ParseX25519Recipient(s)
	if err != nil {
		return nil, err
	}
	return []age.Recipient{r}, nil
}
