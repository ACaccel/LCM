package api

import (
	"github.com/gin-gonic/gin"
)

func InitApi(r *gin.RouterGroup) {
	r.POST("/webhook", postWebhook)
	r.GET("/policy/config", getPolicyConfig)
	r.POST("/policy/config", postPolicyConfig)
	r.GET("/policy/deploy", getPolicyDeploy)
	r.POST("/policy/deploy", postPolicyDeploy)
}