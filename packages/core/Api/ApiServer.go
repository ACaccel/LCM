package api

import (
	"github.com/gin-gonic/gin"

	svc "lcm/packages/core/Service"
)

type Server struct {
	Router   *gin.Engine
	Api      *gin.RouterGroup
	Services map[string]*svc.Service
}

func InitServer() *Server {
	return &Server{
		Router:   gin.Default(),
		Services: make(map[string]*svc.Service, 0),
	}
}

func (s *Server) InitApi() {
	s.Api = s.Router.Group("/api")

	s.Api.POST("/webhook", s.postWebhook)
	s.Api.GET("/policy/config", s.getPolicyConfig)
	s.Api.POST("/policy/config", s.postPolicyConfig)
	s.Api.GET("/policy/deploy", s.getPolicyDeploy)
	s.Api.POST("/policy/deploy", s.postPolicyDeploy)

	s.Api.GET("/upload", s.getUpload)
	s.Api.POST("/upload", s.postUpload)
}

func (s *Server) Serve() {
	s.InitApi()
	s.Router.Run("localhost:8888")
}
