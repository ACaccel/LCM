package apiserver

import (
	"1935/efo"
	"net/http"

	i "1935/efo/LCM_Enabler/interfaces"

	"github.com/gin-gonic/gin"
)

type Put_apis struct {
	efo *efo.EFO
}

func Start_Put_apis(E *efo.EFO) *Put_apis {
	return &Put_apis{efo: E}
}

func (p_api *Put_apis) Configure_Service(c *gin.Context) {
	var config i.ServiceConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.efo.Configure_Service(config.SvcName, config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "configure service success"})
}

func (p_api *Put_apis) Configure_Service_Config(c *gin.Context) {
	// upload config file
	dst := ""
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
	dst = "./efo/LCM_Enabler/upload/" + file.Filename
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}

	if err := p_api.efo.Configure_Service_Config(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
}

func (p_api *Put_apis) Update_Policy(c *gin.Context) {
	service_name := c.Param("service_Name")
	policy_name := c.Param("policy_Name")
	var policy i.PolicyConfig
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.efo.Update_Policy(service_name, policy_name, policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "update policy success"})
}
