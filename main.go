package main

import (
	efo "1935/efo"
	apiserver "1935/efo/External_API/apiserver"
	lcmenabler "1935/efo/LCM_Enabler"
	ruleframework "1935/efo/Rule_Framework"
	"log"
)

func main() {
	// Declaration of the Rule_Framework runtime engine and the LCM_Enabler.
	rf := ruleframework.Build_Rule_Framework()
	LCM := lcmenabler.Build_LCM(rf)

	// Declaration of EFO.
	EFO, err := efo.EFO_Factory(LCM)
	if err != nil {
		log.Fatal(err)
	}

	// Declaration of the External Api Server.
	server := apiserver.ApiserverFactory(EFO)

	// Start the API server for the listening.
	server.Serve()
}
