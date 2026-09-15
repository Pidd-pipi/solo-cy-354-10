package service

import (
	"context"
	"testing"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
)

// seedAppt inserts an appointment in a given status directly into the fake,
// bypassing the service so boundary states are easy to set up.
func seedAppt(appts *fakeAppointmentRepo, orderID, proposer, counterpart uint, status string) *model.MeetupAppointment {
	a := &model.MeetupAppointment{
		OrderID: orderID, ProposerID: proposer, CounterpartID: counterpart,
		MeetAt: time.Now().Add(24 * time.Hour), Location: "图书馆门口", Status: status,
	}
	if err := appts.Create(context.Background(), a); err != nil {
		panic(err)
	}
	return a
}

// TestMeetupBoundaryCreateOrderMissing covers create against a missing order.
func TestMeetupBoundaryCreateOrderMissing(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	_, err := svc.Create(context.Background(), meetupBuyerID, 999, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if got := appErrorStatus(err); got != 404 {
		t.Fatalf("create on missing order want 404, got %d (%v)", got, err)
	}
}

// TestMeetupBoundaryAppointmentNotFound covers all four appointment actions
// against a missing appointment id.
func TestMeetupBoundaryAppointmentNotFound(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	if _, err := svc.Accept(context.Background(), meetupSellerID, 999); appErrorStatus(err) != 404 {
		t.Fatalf("accept missing want 404, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reject(context.Background(), meetupSellerID, 999); appErrorStatus(err) != 404 {
		t.Fatalf("reject missing want 404, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reschedule(context.Background(), meetupSellerID, 999, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "东门"}); appErrorStatus(err) != 404 {
		t.Fatalf("reschedule missing want 404, got %d", appErrorStatus(err))
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, 999); appErrorStatus(err) != 404 {
		t.Fatalf("handover missing want 404, got %d", appErrorStatus(err))
	}
}

// TestMeetupBoundaryRespondOnCancelledOrder covers accept/reject/reschedule on
// a pending appointment whose order was cancelled in the meantime.
func TestMeetupBoundaryRespondOnCancelledOrder(t *testing.T) {
	svc, appts, _, _ := newMeetupFixture(constants.TradeStatusCancelled)
	appt := seedAppt(appts, meetupOrderID, meetupBuyerID, meetupSellerID, constants.AppointmentStatusPending)
	if _, err := svc.Accept(context.Background(), meetupSellerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("accept on cancelled order want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reject(context.Background(), meetupSellerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("reject on cancelled order want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reschedule(context.Background(), meetupSellerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "东门"}); appErrorStatus(err) != 409 {
		t.Fatalf("reschedule on cancelled order want 409, got %d", appErrorStatus(err))
	}
	fresh, _ := appts.FindByID(context.Background(), appt.ID)
	if fresh.Status != constants.AppointmentStatusPending {
		t.Fatalf("appointment status changed on cancelled order: %s", fresh.Status)
	}
}

// TestMeetupBoundaryHandoverStatuses covers handover confirmation on every
// non-accepted appointment status.
func TestMeetupBoundaryHandoverStatuses(t *testing.T) {
	for _, status := range []string{
		constants.AppointmentStatusPending,
		constants.AppointmentStatusRejected,
		constants.AppointmentStatusSuperseded,
		constants.AppointmentStatusCompleted,
	} {
		t.Run(status, func(t *testing.T) {
			svc, appts, _, _ := newMeetupFixture(constants.TradeStatusPending)
			appt := seedAppt(appts, meetupOrderID, meetupBuyerID, meetupSellerID, status)
			if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 409 {
				t.Fatalf("handover on %s want 409, got %d", status, appErrorStatus(err))
			}
		})
	}
}

// TestMeetupBoundaryRejectByProposer covers the proposer trying to reject
// their own proposal.
func TestMeetupBoundaryRejectByProposer(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appt, _ := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if _, err := svc.Reject(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 403 {
		t.Fatalf("proposer reject want 403, got %d", appErrorStatus(err))
	}
}

// TestMeetupBoundaryRescheduleRules covers reschedule on a non-pending
// appointment and with invalid payloads.
func TestMeetupBoundaryRescheduleRules(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appt, _ := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if _, err := svc.Reschedule(context.Background(), meetupSellerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "  "}); appErrorStatus(err) != 400 {
		t.Fatalf("reschedule empty location want 400, got %d", appErrorStatus(err))
	}
	if _, err := svc.Accept(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("accept failed: %v", err)
	}
	if _, err := svc.Reschedule(context.Background(), meetupSellerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "东门"}); appErrorStatus(err) != 409 {
		t.Fatalf("reschedule accepted appointment want 409, got %d", appErrorStatus(err))
	}
}

// TestMeetupBoundaryHandoverSellerFirst completes the dual confirmation in
// the opposite order: seller first, buyer second.
func TestMeetupBoundaryHandoverSellerFirst(t *testing.T) {
	svc, appts, orders, products := newMeetupFixture(constants.TradeStatusConfirmed)
	appt := seedAppt(appts, meetupOrderID, meetupBuyerID, meetupSellerID, constants.AppointmentStatusAccepted)
	if _, err := svc.HandoverConfirm(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("seller first confirm failed: %v", err)
	}
	if orders.orders[meetupOrderID].Status == constants.TradeStatusCompleted {
		t.Fatalf("order completed after single confirmation")
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); err != nil {
		t.Fatalf("buyer second confirm failed: %v", err)
	}
	if got := orders.orders[meetupOrderID].Status; got != constants.TradeStatusCompleted {
		t.Fatalf("order want completed, got %s", got)
	}
	if got := products.statuses[10]; got != constants.ProductStatusSold {
		t.Fatalf("product want sold, got %s", got)
	}
	fresh, _ := appts.FindByID(context.Background(), appt.ID)
	if fresh.Status != constants.AppointmentStatusCompleted {
		t.Fatalf("appointment want completed, got %s", fresh.Status)
	}
}

// TestMeetupBoundaryHandoverOnCancelledOrder covers handover confirmation on
// an order that is already cancelled: every confirmation attempt must fail
// and no state may change.
func TestMeetupBoundaryHandoverOnCancelledOrder(t *testing.T) {
	svc, appts, orders, products := newMeetupFixture(constants.TradeStatusCancelled)
	appt := seedAppt(appts, meetupOrderID, meetupBuyerID, meetupSellerID, constants.AppointmentStatusAccepted)
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("buyer confirm on cancelled order want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupSellerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("seller confirm on cancelled order want 409, got %d", appErrorStatus(err))
	}
	fresh, _ := appts.FindByID(context.Background(), appt.ID)
	if fresh.BuyerConfirmedAt != nil || fresh.SellerConfirmedAt != nil {
		t.Fatalf("confirmation timestamps set on cancelled order: %+v", fresh)
	}
	if got := orders.orders[meetupOrderID].Status; got != constants.TradeStatusCancelled {
		t.Fatalf("order want cancelled, got %s", got)
	}
	if got := products.statuses[10]; got != constants.ProductStatusOnSale {
		t.Fatalf("product want on_sale, got %s", got)
	}
}

// TestMeetupBoundaryHandoverLockedOrderCancelled simulates an order cancel
// committing between the handover pre-check and the locked re-check.
func TestMeetupBoundaryHandoverLockedOrderCancelled(t *testing.T) {
	svc, appts, orders, products := newMeetupFixture(constants.TradeStatusPending)
	orders.lockedStatus[meetupOrderID] = constants.TradeStatusCancelled
	appt := seedAppt(appts, meetupOrderID, meetupBuyerID, meetupSellerID, constants.AppointmentStatusAccepted)
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("handover with locked cancelled order want 409, got %d", appErrorStatus(err))
	}
	fresh, _ := appts.FindByID(context.Background(), appt.ID)
	if fresh.BuyerConfirmedAt != nil {
		t.Fatalf("buyer timestamp set despite cancelled order")
	}
	if got := products.statuses[10]; got != constants.ProductStatusOnSale {
		t.Fatalf("product want on_sale, got %s", got)
	}
}

// TestMeetupBoundaryCreateLockedTerminalOrder covers the locked re-check
// seeing every terminal order status between pre-check and insert.
func TestMeetupBoundaryCreateLockedTerminalOrder(t *testing.T) {
	for _, status := range []string{constants.TradeStatusCompleted, constants.TradeStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			svc, appts, orders, _ := newMeetupFixture(constants.TradeStatusConfirmed)
			orders.lockedStatus[meetupOrderID] = status
			_, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
			if got := appErrorStatus(err); got != 409 {
				t.Fatalf("create with locked %s order want 409, got %d (%v)", status, got, err)
			}
			if n := len(appts.byID); n != 0 {
				t.Fatalf("expected no appointment rows, got %d", n)
			}
		})
	}
}
