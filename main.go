package main

import (
	efo "1935/efo"
	apiserver "1935/efo/External_API/apiserver"
	lcmenabler "1935/efo/LCM_Enabler"
	"log"
)

func main() {
	// Declaration of the LCM_Enabler.
	LCM := lcmenabler.Build_LCM()

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
