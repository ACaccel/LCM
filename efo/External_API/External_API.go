/*
External API in the EFO is provided for the edge service operators or other entities(owner, provider, end user) to access the functionalities of the EFO.
*/

package externalapi

import (
	"1935/efo/External_API/apiserver"
)

type IExternal_API interface {
	Serve()
}

var _ IExternal_API = &apiserver.Apiserver{}
