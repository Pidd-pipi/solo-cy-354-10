package dto

// CreateMeetupAppointmentRequest proposes a face-to-face handover appointment.
// MeetAt accepts "2006-01-02 15:04", "2006-01-02 15:04:05" or RFC3339.
type CreateMeetupAppointmentRequest struct {
	MeetAt   string `json:"meet_at" binding:"required"`
	Location string `json:"location" binding:"required"`
}

// RescheduleMeetupAppointmentRequest proposes a new time/location in place of
// the appointment being responded to.
type RescheduleMeetupAppointmentRequest struct {
	MeetAt   string `json:"meet_at" binding:"required"`
	Location string `json:"location" binding:"required"`
}
