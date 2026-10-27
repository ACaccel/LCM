package efo

import (
	lcmenabler "1935/efo/LCM_Enabler"
)

type EFO struct {
	LCM_Enabler *lcmenabler.LCM_Enabler
}

func EFO_Factory(LCM_Enabler *lcmenabler.LCM_Enabler) (*EFO, error) {
	return &EFO{LCM_Enabler: LCM_Enabler}, nil
}
