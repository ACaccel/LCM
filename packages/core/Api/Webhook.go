package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

    i "lcm/packages/interfaces"
)

func (s *Server)postWebhook(c *gin.Context) {
	var req i.WebhookEvent
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook format"})
		return
	}
	log.Println("Post Webhook: Received webhook", req)

	// process the Policy
    // for _, policy := range core.policies {
    //     for _, trigger := range policy.Triggers {
    //         if trigger.NodeType == "Webhook" {
    //             headNode := policy.PolicyList.NodeSet[trigger.NodeName]
    //             policy.Execute(headNode, req.Content)
    //         }
    //     }
    // }
	for _, policy := range s.Services[req.SvcName].Policies {
		for _, trigger := range policy.Triggers {
			if trigger.NodeType == "Webhook" {
				headNode := policy.PolicyList.NodeSet[trigger.NodeName]
				policy.Execute(headNode, req.Content)
			}
		}
	}
}