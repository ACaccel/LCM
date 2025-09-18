package apiserver

import (
	"1935/efo"

	i "1935/efo/LCM_Enabler/interfaces"
	"net/http"
	"os"

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
	dst := ""
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
	dst = "./efo/LCM_Enabler/upload/" + file.Filename
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}

	if err := p_api.e.Register_Service_Config(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "register service success"})
}

func (p_api *Post_apis) Upload_Service_File(c *gin.Context) {
	// upload manifest file
	service_name := c.Param("service_Name")
	uploadDir := "./efo/LCM_Enabler/upload/" + service_name
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

	// process the Policy
	if err := p_api.e.Execute_Webhook(
		webhook.SvcName,
		webhook.EventType,
		webhook.Content,
	); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	} else {
		c.JSON(http.StatusOK, nil)
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

func (p_api *Post_apis) Configure_DataPacket(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	var data_packet i.ConfigureDataPacket
	if err := c.ShouldBindJSON(&data_packet); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid DataPacket format"})
		return
	}

	if err := p_api.e.Configure_DataPacket(service, policy, data_packet); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "configure DataPacket success"})
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