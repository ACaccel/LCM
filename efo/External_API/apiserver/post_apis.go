package apiserver

import (
	"1935/efo"
	lcmconfig "1935/efo/LCM_Enabler/config"
	i "1935/efo/LCM_Enabler/interfaces"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

type Post_apis struct {
	e *efo.EFO
}

func Start_Post_apis(E *efo.EFO) *Post_apis {
	return &Post_apis{e: E}
}

func (p_api *Post_apis) Register_Service(c *gin.Context) {
	var config i.ServiceConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Register_Service(config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "register service success"})
}

func (p_api *Post_apis) Register_Service_Config(c *gin.Context) {
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

	config, err := p_api.e.Register_Service_Config(dst)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"msg":          "register service success",
		"service_name": config.SvcName,
		"policies":     policyNames(config),
	})
}

// policyNames lists a config's policy names in document order, for the upload
// endpoints' responses. Document order matters: the GUI opens the first one.
func policyNames(config i.ServiceConfig) []string {
	names := make([]string, 0, len(config.Policy))
	for _, policy := range config.Policy {
		names = append(names, policy.PolicyName)
	}
	return names
}

func (p_api *Post_apis) Upload_Service_File(c *gin.Context) {
	// upload manifest file
	service_name := c.Param("service_Name")
	uploadDir := "./" + lcmconfig.UploadDir() + "/" + service_name
	if _, err := os.Stat(uploadDir); os.IsNotExist(err) {
		if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files := form.File["file"]
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}
	for _, file := range files {
		dst := uploadDir + "/" + file.Filename
		if err := c.SaveUploadedFile(file, dst); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"msg": "upload service manifest files success"})
}

func (p_api *Post_apis) Execute_Webhook(c *gin.Context) {
	var webhook i.WebhookEvent
	if err := c.ShouldBindJSON(&webhook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// dispatch the event to subscribed webhook triggers
	dispatched, err := p_api.e.Execute_Webhook(
		webhook.SvcName,
		webhook.EventType,
		webhook.Content,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, gin.H{"dispatched": dispatched})
	}
}

func (p_api *Post_apis) Register_Policy(c *gin.Context) {
	service_name := c.Param("service_Name")
	var policy i.PolicyConfig
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Register_Policy(service_name, policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, gin.H{"msg": "register policy success"})
	}
}

func (p_api *Post_apis) Policy_Deployment(c *gin.Context) {
	var deploy_data i.DeployPolicy
	if err := c.ShouldBindJSON(&deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Policy_Deployment(deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, nil)
	}
}

func (p_api *Post_apis) Policy_Undeployment(c *gin.Context) {
	var deploy_data i.DeployPolicy
	if err := c.ShouldBindJSON(&deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Policy_Undeployment(deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, nil)
	}
}

func (p_api *Post_apis) Configure_Template(c *gin.Context) {
	var template_data i.TemplateConfigure
	if err := c.ShouldBindJSON(&template_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Configure_Template(template_data.SvcName, template_data.TemplateName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "configure template success"})
}

func (p_api *Post_apis) Register_Module(c *gin.Context) {
	service_name := c.Param("service_Name")
	var module i.ModuleConfig
	if err := c.ShouldBindJSON(&module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := p_api.e.Register_Module(service_name, module); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, gin.H{"msg": "register module success"})
	}
}
