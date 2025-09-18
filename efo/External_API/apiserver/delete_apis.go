package apiserver

import (
	"1935/efo"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Delete_apis struct {
	efo *efo.EFO
}

func Start_Delete_apis(E *efo.EFO) *Delete_apis {
	return &Delete_apis{efo: E}
}

func (d_api *Delete_apis) Remove_Service(c *gin.Context) {
	service := c.Param("service_Name")

	err := d_api.efo.Remove_Service(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	
	c.JSON(http.StatusOK, gin.H{ "msg": "terminate service success" })
}

func (d_api *Delete_apis) Remove_Policy(c *gin.Context) {
	service_name := c.Param("service_Name")
	policy_name := c.Param("policy_Name")

	err := d_api.efo.Remove_Policy(service_name, policy_name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	
	c.JSON(http.StatusOK, gin.H{ "msg": "remove policy success" })
}