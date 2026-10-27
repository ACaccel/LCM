package apiserver

import (
	"1935/efo"

	"github.com/gin-gonic/gin"
)

type Apiserver struct {
	e      *efo.EFO
	post   *Post_apis
	put    *Put_apis
	get    *Get_apis
	delete *Delete_apis
}

func ApiserverFactory(E *efo.EFO) *Apiserver {
	return &Apiserver{
		e:      E,
		post:   Start_Post_apis(E),
		put:    Start_Put_apis(E),
		get:    Start_Get_apis(E),
		delete: Start_Delete_apis(E),
	}
}

func (A *Apiserver) Serve() {
	route := gin.Default()

	route.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	// The auth middleware of the testbed is intentionally not carried over:
	// the LCM API is unauthenticated, as in the source snapshot.
	api := route.Group("/api")
	{
		// LCM relevant APIs:
		api.GET("/lcm/service", A.get.List_Services)
		api.GET("/lcm/nodetypes", A.get.Node_Types)
		api.GET("/lcm/service/:service_Name", A.get.Service_Information)
		api.POST("/lcm/service", A.post.Register_Service)
		api.PUT("/lcm/service", A.put.Configure_Service)
		api.POST("/lcm/service/file", A.post.Register_Service_Config)
		api.PUT("/lcm/service/file", A.put.Configure_Service_Config)
		api.POST("/lcm/service/:service_Name/file", A.post.Upload_Service_File)
		api.DELETE("/lcm/service/:service_Name", A.delete.Remove_Service)

		api.GET("/lcm/service/:service_Name/module", A.get.List_Modules)
		api.GET("/lcm/service/:service_Name/module/:module_Name", A.get.Module_Information)
		api.POST("/lcm/service/:service_Name/module", A.post.Register_Module)
		api.PUT("/lcm/service/:service_Name/module/:module_Name", A.put.Update_Module)
		api.DELETE("/lcm/service/:service_Name/module/:module_Name", A.delete.Remove_Module)

		api.GET("/lcm/service/:service_Name/policy/:policy_Name", A.get.Policy_Information)
		api.POST("/lcm/service/:service_Name/policy", A.post.Register_Policy)
		api.POST("/lcm/service/policy/deploy", A.post.Policy_Deployment)
		api.POST("/lcm/service/policy/undeploy", A.post.Policy_Undeployment)
		api.PUT("/lcm/service/:service_Name/policy/:policy_Name", A.put.Update_Policy)
		api.DELETE("/lcm/service/:service_Name/policy/:policy_Name", A.delete.Remove_Policy)

		api.GET("/lcm/service/:service_Name/policy/:policy_Name/run", A.get.Policy_Run)

		api.POST("/lcm/service/template", A.post.Configure_Template)

		api.POST("/lcm/webhook", A.post.Execute_Webhook)
	}

	route.Run("127.0.0.1:8000")
}
