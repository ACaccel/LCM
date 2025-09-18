package apiserver

import (
	"1935/efo"
)

type Patch_apis struct {
	e *efo.EFO
}

func Start_Patch_apis(E *efo.EFO) *Patch_apis {
	return &Patch_apis{e: E}
}
