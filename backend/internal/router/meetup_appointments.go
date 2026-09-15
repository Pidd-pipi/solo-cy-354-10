package router

import (
	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/handler"
)

// RegisterMeetupAppointmentRoutes registers meetup appointment endpoints.
func RegisterMeetupAppointmentRoutes(g *gin.RouterGroup, h *handler.MeetupAppointmentHandler, auth, apiLimiter gin.HandlerFunc) {
	g.POST("/trade-orders/:id/appointments", auth, apiLimiter, h.Create)
	g.GET("/trade-orders/:id/appointments", auth, apiLimiter, h.ListByOrder)
	appts := g.Group("/appointments", auth)
	{
		appts.POST("/:id/accept", apiLimiter, h.Accept)
		appts.POST("/:id/reject", apiLimiter, h.Reject)
		appts.POST("/:id/reschedule", apiLimiter, h.Reschedule)
		appts.POST("/:id/handover-confirm", apiLimiter, h.HandoverConfirm)
	}
}
