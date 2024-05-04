package main

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"os"
	"path"

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

	s.Api.POST("/webhook", s.postWebhook)
	s.Api.POST("/policy/deploy", s.postPolicyDeploy)
	s.Api.GET("/upload", s.getUpload)
	s.Api.POST("/upload", s.postUpload)
}

func (s *Server) Serve() {
	s.InitApi()
	s.Router.Run("localhost:8888")
}

/* request handlers */

func (s *Server) postWebhook(c *gin.Context) {
	var req i.WebhookEvent
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook format"})
		return
	}
	log.Println("Post Webhook: Received webhook", req)

	// process the Policy
	s.LCM.Execute_Webhook(req)
}

func (s *Server) postPolicyDeploy(c *gin.Context) {
	// parse request
	var req struct {
		SvcName  string   `json:"SvcName"`
		Type     string   `json:"Type"`
		Policies []string `json:"Policies"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy format"})
		return
	}
	log.Println("Policy Deploy: Received policy deploy post request", req)

	switch req.Type {
	case "POLICY_DEPLOY":
		err := s.LCM.Deploy_Policy(req.SvcName, req.Policies)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deployed Policies": req.Policies})
	case "POLICY_UNDEPLOY":
		err := s.LCM.Undeploy_Policy(req.SvcName, req.Policies)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"undeployed Policies": req.Policies})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy type"})
	}
}

func (s *Server) getUpload(c *gin.Context) {
	s.Router.LoadHTMLFiles("./upload.html")
	c.HTML(http.StatusOK, "upload.html", nil)
}

func (s *Server) postUpload(c *gin.Context) {
	file, err := c.FormFile("config")

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
	} else {
		dst := path.Join("./upload/", file.Filename)
		c.SaveUploadedFile(file, dst)

		// rename file with service name
		var config i.ServiceConfig
		err := utils.ReadYaml(dst, &config)
		if err != nil {
			log.Println("Failed to parse YAML file")
		}

		os.Rename(dst, path.Join("./upload/", fmt.Sprintf("%s.yaml", config.SvcName)))

		// register service
		s.LCM.Register_Service(config)

		log.Println("File uploaded: Received config file from service", config.SvcName)
		c.JSON(200, gin.H{
			"msg": "success",
		})
	}
}
