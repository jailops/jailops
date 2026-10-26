package zfsadapt

import (
	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/lifecycle"
)

var (
	_ lifecycle.ZFS = (*Adapter)(nil)
	_ imagegen.ZFS  = (*Adapter)(nil)
)
