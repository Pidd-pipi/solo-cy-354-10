package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/middleware"
	"github.com/lp/campus-market/internal/service"
	"github.com/lp/campus-market/internal/util"
)

// MeetupAppointmentHandler exposes meetup appointment endpoints.
type MeetupAppointmentHandler struct {
	svc    *service.MeetupAppointmentService
	logger *slog.Logger
}

// NewMeetupAppointmentHandler wires the meetup appointment handler dependencies.
func NewMeetupAppointmentHandler(svc *service.MeetupAppointmentService, logger *slog.Logger) *MeetupAppointmentHandler {
	return &MeetupAppointmentHandler{svc: svc, logger: logger}
}

// Create handles POST /trade-orders/:id/appointments.
func (h *MeetupAppointmentHandler) Create(c *gin.Context) {
	userID, orderID, ok := h.userAndOrder(c)
	if !ok {
		return
	}
	var req dto.CreateMeetupAppointmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeValidation, constants.MsgValidationFailed)
		return
	}
	appt, err := h.svc.Create(c.Request.Context(), userID, orderID, &req)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, appt)
}

// ListByOrder handles GET /trade-orders/:id/appointments.
func (h *MeetupAppointmentHandler) ListByOrder(c *gin.Context) {
	userID, orderID, ok := h.userAndOrder(c)
	if !ok {
		return
	}
	items, err := h.svc.ListByOrder(c.Request.Context(), userID, orderID)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, items)
}

// Accept handles POST /appointments/:id/accept.
func (h *MeetupAppointmentHandler) Accept(c *gin.Context) {
	h.act(c, func(userID, appointmentID uint) (interface{}, error) {
		return h.svc.Accept(c.Request.Context(), userID, appointmentID)
	})
}

// Reject handles POST /appointments/:id/reject.
func (h *MeetupAppointmentHandler) Reject(c *gin.Context) {
	h.act(c, func(userID, appointmentID uint) (interface{}, error) {
		return h.svc.Reject(c.Request.Context(), userID, appointmentID)
	})
}

// Reschedule handles POST /appointments/:id/reschedule.
func (h *MeetupAppointmentHandler) Reschedule(c *gin.Context) {
	userID, appointmentID, ok := h.userAndAppointment(c)
	if !ok {
		return
	}
	var req dto.RescheduleMeetupAppointmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeValidation, constants.MsgValidationFailed)
		return
	}
	appt, err := h.svc.Reschedule(c.Request.Context(), userID, appointmentID, &req)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, appt)
}

// HandoverConfirm handles POST /appointments/:id/handover-confirm.
func (h *MeetupAppointmentHandler) HandoverConfirm(c *gin.Context) {
	h.act(c, func(userID, appointmentID uint) (interface{}, error) {
		return h.svc.HandoverConfirm(c.Request.Context(), userID, appointmentID)
	})
}

func (h *MeetupAppointmentHandler) act(c *gin.Context, fn func(userID, appointmentID uint) (interface{}, error)) {
	userID, appointmentID, ok := h.userAndAppointment(c)
	if !ok {
		return
	}
	appt, err := fn(userID, appointmentID)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, appt)
}

func (h *MeetupAppointmentHandler) userAndOrder(c *gin.Context) (uint, uint, bool) {
	userID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return 0, 0, false
	}
	orderID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeBadRequest, "订单ID不合法")
		return 0, 0, false
	}
	return userID, uint(orderID), true
}

func (h *MeetupAppointmentHandler) userAndAppointment(c *gin.Context) (uint, uint, bool) {
	userID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return 0, 0, false
	}
	appointmentID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeBadRequest, "预约ID不合法")
		return 0, 0, false
	}
	return userID, uint(appointmentID), true
}
