package apiserver

import (
	"1935/efo"
	lcmconfig "1935/efo/LCM_Enabler/config"
	i "1935/efo/LCM_Enabler/interfaces"
	"net/http"
	"path/filepath"

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
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Base strips any client-supplied directory part so the file cannot land
	// outside the upload dir.
	dst := "./" + lcmconfig.UploadDir() + "/" + filepath.Base(file.Filename)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	config, err := p_api.efo.Configure_Service_Config(dst)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"msg":          "configure service success",
		"service_name": config.SvcName,
		"policies":     policyNames(config),
	})
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

func (p_api *Put_apis) Update_Module(c *gin.Context) {
	service_name := c.Param("service_Name")
	module_name := c.Param("module_Name")
	var module i.ModuleConfig
	if err := c.ShouldBindJSON(&module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.efo.Update_Module(service_name, module_name, module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "update module success"})
}
