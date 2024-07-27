package main

import (
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

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
		LCM:    lcm.Build_LCM(),
	}
}

func (s *Server) InitApi() {
	s.Api = s.Router.Group("/api")

	s.Api.GET("/service/:service_Name", s.Service_Information)
	s.Api.POST("/service", s.Register_Service)
	s.Api.PUT("/service", s.Configure_Service)
	s.Api.POST("/service/file", s.Register_Service_File)
	s.Api.PUT("/service/file", s.Configure_Service_File)
	s.Api.DELETE("/service/:service_Name", s.Terminate_Service)

	s.Api.GET("/service/:service_Name/policy/:policy_Name", s.Policy_Information)
	s.Api.POST("/service/policy/deploy", s.Policy_Deployment)

	s.Api.GET("/service/:service_Name/policy/:policy_Name/datapacket", s.DataPacket_Information) 
	s.Api.POST("/service/:service_Name/policy/:policy_Name/datapacket", s.Configure_DataPacket)

	s.Api.POST("/webhook", s.Execute_Webhook)
}

func (s *Server) Serve() {
	s.InitApi()
	s.Router.Run("localhost:8000")
}

/* request handlers */
func (s *Server) Service_Information(c *gin.Context) {
	service := c.Param("service_Name")

	info, err := s.LCM.Service_Information(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (s *Server) Register_Service(c *gin.Context) {
	var config i.ServiceConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Register_Service(config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	c.JSON(http.StatusOK, gin.H{ "msg": "register service success" })
}

func (s *Server) Configure_Service(c *gin.Context) {
	var config i.ServiceConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Configure_Service(config.SvcName, config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	c.JSON(http.StatusOK, gin.H{ "msg": "configure service success" })
}

func (s *Server) Register_Service_File(c *gin.Context) {
	dst, err := upload_config(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	config, err := rename_config(dst, c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Register_Service(config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	c.JSON(http.StatusOK, gin.H{ "msg": "register service success" })
}

func (s *Server) Configure_Service_File(c *gin.Context) {
	dst, err := upload_config(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	config, err := rename_config(dst, c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	if err := s.LCM.Configure_Service(config.SvcName, config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}

	c.JSON(http.StatusOK, gin.H{ "msg": "configure service success" })
}

func (s *Server) Terminate_Service(c *gin.Context) {
	service := c.Param("service_Name")

	err := s.LCM.Terminate_Service(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return
	}
	
	c.JSON(http.StatusOK, gin.H{ "msg": "terminate service success" })
}

func (s *Server) Policy_Information(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	info, err := s.LCM.Policy_Information(service, policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (s *Server) Policy_Deployment(c *gin.Context) {
	// parse request
	var deploy_data i.DeployPolicy
	if err := c.ShouldBindJSON(&deploy_data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PolicyDeploy format"})
		return
	}

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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PolicyDeploy type"})
	}
}

func (s *Server) DataPacket_Information(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")
	node := c.Query("node")
	port_str := c.Query("port")

	port, err := strconv.Atoi(port_str)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	info, err := s.LCM.DataPacket_Information(service, policy, node, port)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (s *Server) Configure_DataPacket(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	var data_packet i.ConfigureDataPacket
	if err := c.ShouldBindJSON(&data_packet); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid DataPacket format"})
		return
	}

	if err := s.LCM.Configure_DataPacket(service, policy, data_packet); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"msg": "configure DataPacket success"})
}

func (s *Server) Execute_Webhook(c *gin.Context) {
	var webhook i.WebhookEvent
	if err := c.ShouldBindJSON(&webhook); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook format"})
		return
	}

	// process the Policy
	if err := s.LCM.Execute_Webhook(
		webhook.SvcName,
		webhook.EventType,
		webhook.Content,
	); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
	} else {
		c.JSON(http.StatusOK, gin.H{ "msg": "webhook executed" })
	}
}

/* utility functions */
func upload_config(c *gin.Context) (string, error) {
	file, err := c.FormFile("config")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return "", err
	}
	dst := "./upload/" + file.Filename
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return "", err
	}
	return dst, nil
}

func rename_config(dst string, c *gin.Context) (i.ServiceConfig, error) {
	var config i.ServiceConfig
	if err := utils.ReadYaml(dst, &config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return config, err
	}
	rename_dst := "./upload/" + config.SvcName + ".yaml"
	if err := os.Rename(dst, rename_dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{ "error": err.Error() })
		return config, err
	}
	return config, nil
}