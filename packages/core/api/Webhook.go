package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

    i "interfaces"
)

func postWebhook(c *gin.Context) {
	reqBody := i.WebhookEvent{}

	if err := c.ShouldBindJSON(&reqBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook format"})
		return
	}

	log.Println("Post Webhook: Received webhook", reqBody)

	// process the Policy
    for _, policy := range core.policies {
        for _, trigger := range policy.Triggers {
            if trigger.NodeType == "Webhook" {
                headNode := policy.PolicyList.NodeSet[trigger.NodeName]
                policy.Execute(headNode, reqBody.Content)
            }
        }
    }
}