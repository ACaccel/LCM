package main

import (
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"os"

	lcm "lcm/packages"
	i "lcm/packages/interfaces"
	utils "lcm/packages/utils"
)

type Server struct {
	Router *gin.Engine
	Api    *gin.RouterGroup
	LCM    *lcm.LCM_Enabler
}

func InitServer() *Server {
	return &Server{
		Router: gin.Default(),
		LCM:    &lcm.LCM_Enabler{
			Services: make(map[string]*lcm.Service),
		},
	}
}

func (s *Server) InitApi() {
	s.Api = s.Router.Group("/api")

	s.Api.POST("/webhook", s.Execute_Webhook)
	s.Api.POST("/policy/deploy", s.postPolicyDeploy)
	s.Api.GET("/upload", s.getUpload)
	s.Api.POST("/upload", s.Upload_Config)
	s.Api.POST("/configure", s.Configure_Service)
}

func (s *Server) Serve() {
	s.InitApi()
	s.Router.Run("localhost:8000")
}

/* request handlers */

func (s *Server) Execute_Webhook(c *gin.Context) {
	var webhook i.WebhookEvent
	if err := c.ShouldBindJSON(&webhook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook format"})
		return
	}
	log.Println("Post Webhook: Received webhook", webhook)

	// process the Policy
	if err := s.LCM.Execute_Webhook(
		webhook.SvcName,
		webhook.EventType,
		webhook.Content,
	); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
	} else {
		c.JSON(http.StatusOK, nil)
	}
}

func (s *Server) postPolicyDeploy(c *gin.Context) {
	// parse request
	var deploy_data i.DeployPolicy
	if err := c.ShouldBindJSON(&deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy format"})
		return
	}
	log.Println("Policy Deploy: Received policy deploy post request", deploy_data)

	switch deploy_data.Type {
	case "POLICY_DEPLOY":
		if err := s.LCM.Deploy_Policy(deploy_data.SvcName, deploy_data.Policies); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deployed Policies": deploy_data.Policies})
	case "POLICY_UNDEPLOY":
		if err := s.LCM.Undeploy_Policy(deploy_data.SvcName, deploy_data.Policies); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"undeployed Policies": deploy_data.Policies})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy type"})
	}
}

func (s *Server) getUpload(c *gin.Context) {
	s.Router.LoadHTMLFiles("./upload.html")
	c.HTML(http.StatusOK, "upload.html", nil)
}

func (s *Server) Upload_Config(c *gin.Context) {
	file, err := c.FormFile("config")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	dst := "./upload/" + file.Filename
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	// rename file with service name
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	rename_dst := "./upload/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Register_Service(config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	log.Println("File uploaded: Received config file from service", config.SvcName)
	c.JSON(200, gin.H{
		"msg": "success",
	})
}

func (s *Server) Configure_Service(c *gin.Context) {
	file, err := c.FormFile("config")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	dst := "./upload/" + file.Filename
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	// rename file with service name
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	rename_dst := "./upload/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Configure_Service(config.SvcName, config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	log.Println("File uploaded: Received config file from service", config.SvcName)
	c.JSON(200, gin.H{
		"msg": "success",
	})
}