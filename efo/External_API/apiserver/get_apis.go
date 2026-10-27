package apiserver

import (
	"1935/efo"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type Get_apis struct {
	efo *efo.EFO
}

func Start_Get_apis(E *efo.EFO) *Get_apis {
	return &Get_apis{efo: E}
}

func (g_api *Get_apis) Service_Information(c *gin.Context) {
	service := c.Param("service_Name")

	info, err := g_api.efo.Service_Information(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

func (g_api *Get_apis) List_Services(c *gin.Context) {
	c.JSON(http.StatusOK, g_api.efo.List_Services())
}

func (g_api *Get_apis) Node_Types(c *gin.Context) {
	c.JSON(http.StatusOK, g_api.efo.Node_Types())
}

func (g_api *Get_apis) Policy_Information(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	info, err := g_api.efo.Policy_Information(service, policy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

// Policy_Run returns the latest execution trace of a deployed policy. The
// optional since query parameter returns only events newer than that sequence
// number, so a client can poll a running execution incrementally.
func (g_api *Get_apis) Policy_Run(c *gin.Context) {
	service := c.Param("service_Name")
	policy := c.Param("policy_Name")

	since := 0
	if since_str := c.Query("since"); since_str != "" {
		parsed, err := strconv.Atoi(since_str)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		since = parsed
	}

	trace, err := g_api.efo.Policy_Run(service, policy, since)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, trace)
}

func (g_api *Get_apis) List_Modules(c *gin.Context) {
	service := c.Param("service_Name")

	modules, err := g_api.efo.List_Modules(service)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, modules)
}

func (g_api *Get_apis) Module_Information(c *gin.Context) {
	service := c.Param("service_Name")
	module := c.Param("module_Name")

	info, err := g_api.efo.Module_Information(service, module)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}
