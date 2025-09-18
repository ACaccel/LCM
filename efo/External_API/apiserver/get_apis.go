package apiserver

import (
	"1935/efo"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type Get_apis struct {
	efo *efo.EFO
}

func Start_Get_apis(E *efo.EFO) *Get_apis {
	return &Get_apis{efo: E}
}

func (g_api *Get_apis) Service_Information(c *gin.Context) {
	service := c.Param("service_Name")

	info, err := g_api.efo.Service_Information(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (g_api *Get_apis) Policy_Information(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	info, err := g_api.efo.Policy_Information(service, policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (g_api *Get_apis) DataPacket_Information(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")
	node := c.Query("node")
	port_str := c.Query("port")

	port, err := strconv.Atoi(port_str)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info, err := g_api.efo.DataPacket_Information(service, policy, node, port)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}