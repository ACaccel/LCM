package main

import (
    "github.com/gin-gonic/gin"
    
    "core/api"
)

func main() {
    router := gin.Default()

	apiRouter := router.Group("/api")
	api.InitApi(apiRouter)

    router.Run("localhost:8888")
}