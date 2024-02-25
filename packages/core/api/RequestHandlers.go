package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"core/utils"
	i "interfaces"
	p "policy"
)

// Core stores the set of deployed policies
type Core struct {
	policies []p.Policy
}

var core Core = Core{}
// const configPath = "config.json"
const configPath = "config.yaml"

func getPolicyConfig(c *gin.Context) {
	log.Println("Policy Config: Received policy config get request")

	var config []i.PolicyConfig
	err := utils.ReadJson(configPath, &config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse JSON file"})
		return
	}

	c.JSON(http.StatusOK, config)
}

func postPolicyConfig(c *gin.Context) {
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

func getPolicyDeploy(c *gin.Context) {
	var deployedPolicies struct {

	}
	c.IndentedJSON(http.StatusOK, deployedPolicies)
}

func postPolicyDeploy(c *gin.Context) {
	var reqBody struct {
		Type string `json:"type"`
		Policies []string `json:"policies"`
	}
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy format"})
		return
	}
	log.Println("Policy Deploy: Received policy deploy post request", reqBody)

	switch reqBody.Type {
	case "POLICY_DEPLOY":
		var deployedPolicies []string = []string{}
		for _, policyName := range reqBody.Policies {
			newPolicy, err := utils.DeployPolicy(configPath, policyName, core.policies)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			core.policies = append(core.policies, newPolicy)
			deployedPolicies = append(deployedPolicies, policyName)
			log.Println("Policy Deploy: Policy", policyName, "has been deployed")
		}

		c.JSON(http.StatusOK, gin.H{"deployed Policies": deployedPolicies})
	case "POLICY_UNDEPLOY":
		var undeployedPolicies []string = []string{}
		for _, policyName := range reqBody.Policies {
			// check if policy already deployed
			for index, deployedPolicies := range core.policies {
				if deployedPolicies.Description.PolicyName == policyName {
					core.policies = append(core.policies[:index], core.policies[index+1:]...)
					undeployedPolicies = append(undeployedPolicies, policyName)
					log.Println("Policy Deploy: Policy", policyName, "has been undeployed")
					break
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"undeployed Policies": undeployedPolicies})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid postPolicyDeploy type"})
	}
}