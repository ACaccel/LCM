package apiserver

import (
	"1935/efo"

	"github.com/gin-gonic/gin"
)

type Apiserver struct {
	e          *efo.EFO
	post       *Post_apis
	patch      *Patch_apis
	put        *Put_apis
	get        *Get_apis
	delete     *Delete_apis
}

func ApiserverFactory(E *efo.EFO) *Apiserver {
	return &Apiserver{
		e:          E,
		post:       Start_Post_apis(E),
		patch:      Start_Patch_apis(E),
		put:        Start_Put_apis(E),
		get:        Start_Get_apis(E),
		delete:     Start_Delete_apis(E),
	}
}

func (A *Apiserver) Serve() {
	route := gin.Default()

	route.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	// api
	api := route.Group("/api")
	{
		// LCM relevant APIs:
		api.GET("/lcm/service/:service_Name", A.get.Service_Information)
		api.POST("/lcm/service", A.post.Register_Service)
		api.PUT("/lcm/service", A.put.Configure_Service)
		api.POST("/lcm/service/file", A.post.Register_Service_Config)
		api.PUT("/lcm/service/file", A.put.Configure_Service_Config)
		api.POST("/lcm/service/:service_Name/file", A.post.Upload_Service_File)
		api.DELETE("/lcm/service/:service_Name", A.delete.Remove_Service)

		api.GET("/lcm/service/:service_Name/policy/:policy_Name", A.get.Policy_Information)
		api.POST("/lcm/service/:service_Name/policy", A.post.Register_Policy)
		api.POST("/lcm/service/policy/deploy", A.post.Policy_Deployment)
		api.POST("/lcm/service/policy/undeploy", A.post.Policy_Undeployment)
		api.PUT("/lcm/service/:service_Name/policy/:policy_Name", A.put.Update_Policy)
		api.DELETE("/lcm/service/:service_Name/policy/:policy_Name", A.delete.Remove_Policy)

		api.GET("/lcm/service/:service_Name/policy/:policy_Name/datapacket", A.get.DataPacket_Information) 
		api.POST("/lcm/service/:service_Name/policy/:policy_Name/datapacket", A.post.Configure_DataPacket)

		api.POST("/lcm/service/template", A.post.Configure_Template)

		api.POST("/lcm/webhook", A.post.Execute_Webhook)
	}

	route.Run("127.0.0.1:8000")
}
