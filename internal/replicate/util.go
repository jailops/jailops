package replicate

import (
	"fmt"
	"strconv"
)

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func hex16(v uint64) string { return fmt.Sprintf("%016x", v) }
