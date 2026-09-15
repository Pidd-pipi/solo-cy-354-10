package dto

import "github.com/lp/campus-market/internal/model"

// CreateTradeOrderRequest creates a purchase intent for a product.
type CreateTradeOrderRequest struct {
	ProductID uint `json:"product_id" binding:"required"`
}

// TradeOrderView enriches a trade order with its latest meetup appointment so
// the trade list can show the appointment time, location and both parties'
// handover confirmation state.
type TradeOrderView struct {
	model.TradeOrder
	Appointment *model.MeetupAppointment `json:"appointment"`
}
