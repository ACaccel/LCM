package api

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/gin-gonic/gin"

	svc "lcm/packages/core/Service"
	"lcm/packages/core/utils"
	i "lcm/packages/interfaces"
)

const configPath = "config.yaml"

func (s *Server) getUpload(c *gin.Context) {
	s.Router.LoadHTMLFiles("./packages/core/Api/upload.html")
	c.HTML(http.StatusOK, "upload.html", nil)
}

func (s *Server) postUpload(c *gin.Context) {
	f, err := c.FormFile("config")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
	} else {
		dst := path.Join("./upload/", f.Filename)
		c.SaveUploadedFile(f, dst)

		// rename file with service name
		var config i.ServiceConfig
		err := utils.ReadYaml(configPath, &config)
		if err != nil {
			log.Println("Failed to parse YAML file")
		}
		
		os.Rename(dst, path.Join("./upload/", fmt.Sprintf("%s.yaml", config.SvcName)))

		log.Println("File uploaded: Received config file from service", config.SvcName)
		c.JSON(200, gin.H{
			"msg":  "success",
			"name": f.Filename,
			"size": f.Size,
		})
	}
}

func (s *Server) getPolicyConfig(c *gin.Context) {
	log.Println("Policy Config: Received policy config get request")

	var config []i.PolicyConfig
	err := utils.ReadJson(configPath, &config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse JSON file"})
		return
	}

	c.JSON(http.StatusOK, config)
}

func (s *Server) postPolicyConfig(c *gin.Context) {
	var reqBody any
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyConfig format"})
		return
	}
	log.Println("Policy Config: Received policy config post request", reqBody)

	err := utils.WriteJson(configPath, reqBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse JSON file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "OK"})
}

func (s *Server) getPolicyDeploy(c *gin.Context) {
	var deployedPolicies struct {
	}
	c.IndentedJSON(http.StatusOK, deployedPolicies)
}

func (s *Server) postPolicyDeploy(c *gin.Context) {
	// parse request
	var req struct {
		SvcName  string   `json:"svcname"`
		Type     string   `json:"type"`
		Policies []string `json:"policies"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy format"})
		return
	}
	log.Println("Policy Deploy: Received policy deploy post request", req)

	// fetch config file and configure service
	var config i.ServiceConfig
	err := utils.ReadYaml(configPath, &config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse YAML file"})
	}
	newService := svc.InitService(req.SvcName, config)
	s.Services[req.SvcName] = &newService

	switch req.Type {
	case "POLICY_DEPLOY":
		var resPolicies []string
		for _, policyName := range req.Policies {
			err := s.Services[req.SvcName].Deploy(policyName)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			log.Println("Policy Deploy: Policy", policyName, "has been deployed")
			resPolicies = append(resPolicies, policyName)
		}

		c.JSON(http.StatusOK, gin.H{"deployed Policies": resPolicies})
	case "POLICY_UNDEPLOY":
		var resPolicies []string
		for _, policyName := range req.Policies {
			// check if policy already deployed
			err := s.Services[req.SvcName].Undeploy(policyName)
			if err != nil {
				log.Println("Policy Undeploy:", err.Error())
				continue
			}

			log.Println("Policy Deploy: Policy", policyName, "has been undeployed")
			resPolicies = append(resPolicies, policyName)
		}

		c.JSON(http.StatusOK, gin.H{"undeployed Policies": resPolicies})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy type"})
	}
}
