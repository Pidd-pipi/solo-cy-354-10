package model

import "time"

// MeetupAppointment is a face-to-face handover appointment attached to a trade
// order. Exactly one appointment per order may be active (pending/accepted) at
// any time; rejected and superseded appointments are kept as history.
type MeetupAppointment struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	OrderID           uint       `gorm:"index;not null" json:"order_id"`
	ProposerID        uint       `gorm:"index;not null" json:"proposer_id"`
	CounterpartID     uint       `gorm:"index;not null" json:"counterpart_id"`
	MeetAt            time.Time  `gorm:"not null" json:"meet_at"`
	Location          string     `gorm:"size:128;not null" json:"location"`
	Status            string     `gorm:"size:16;index;not null;default:pending" json:"status"`
	BuyerConfirmedAt  *time.Time `json:"buyer_confirmed_at"`
	SellerConfirmedAt *time.Time `json:"seller_confirmed_at"`
	RespondedAt       *time.Time `json:"responded_at"`
	CreatedAt         time.Time  `json:"created_at"`
}
