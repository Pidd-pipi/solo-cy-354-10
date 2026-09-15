package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
)

type fakeAppointmentRepo struct {
	byID      map[uint]*model.MeetupAppointment
	nextID    uint
	createErr error
}

func newFakeAppointmentRepo() *fakeAppointmentRepo {
	return &fakeAppointmentRepo{byID: map[uint]*model.MeetupAppointment{}, nextID: 1}
}

func (f *fakeAppointmentRepo) Create(_ context.Context, a *model.MeetupAppointment) error {
	if f.createErr != nil {
		return f.createErr
	}
	a.ID = f.nextID
	f.nextID++
	cp := *a
	f.byID[a.ID] = &cp
	return nil
}

func (f *fakeAppointmentRepo) FindByID(_ context.Context, id uint) (*model.MeetupAppointment, error) {
	if a, ok := f.byID[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, util.ErrNotFound
}

func (f *fakeAppointmentRepo) FindActiveByOrder(_ context.Context, orderID uint) (*model.MeetupAppointment, error) {
	for _, a := range f.byID {
		if a.OrderID == orderID && constants.IsAppointmentActive(a.Status) {
			cp := *a
			return &cp, nil
		}
	}
	return nil, util.ErrNotFound
}

func (f *fakeAppointmentRepo) ListByOrder(_ context.Context, orderID uint) ([]model.MeetupAppointment, error) {
	var out []model.MeetupAppointment
	for _, a := range f.byID {
		if a.OrderID == orderID {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeAppointmentRepo) UpdateStatus(_ context.Context, id uint, from []string, to string, respondedAt time.Time) error {
	a, ok := f.byID[id]
	if !ok {
		return util.ErrNotFound
	}
	for _, s := range from {
		if a.Status == s {
			a.Status = to
			a.RespondedAt = &respondedAt
			return nil
		}
	}
	return util.ErrConflict
}

func (f *fakeAppointmentRepo) ConfirmHandover(_ context.Context, id uint, column string, ts time.Time) error {
	a, ok := f.byID[id]
	if !ok {
		return util.ErrNotFound
	}
	if a.Status != constants.AppointmentStatusAccepted {
		return util.ErrConflict
	}
	switch column {
	case "buyer_confirmed_at":
		if a.BuyerConfirmedAt != nil {
			return util.ErrConflict
		}
		a.BuyerConfirmedAt = &ts
	case "seller_confirmed_at":
		if a.SellerConfirmedAt != nil {
			return util.ErrConflict
		}
		a.SellerConfirmedAt = &ts
	}
	return nil
}

func (f *fakeAppointmentRepo) MarkCompleted(_ context.Context, id uint) error {
	a, ok := f.byID[id]
	if !ok {
		return util.ErrNotFound
	}
	if a.Status != constants.AppointmentStatusAccepted {
		return util.ErrConflict
	}
	a.Status = constants.AppointmentStatusCompleted
	return nil
}

func (f *fakeAppointmentRepo) Transaction(_ context.Context, fn func(txCtx context.Context) error) error {
	return fn(context.Background())
}

type fakeMeetupOrderRepo struct {
	orders       map[uint]*model.TradeOrder
	lockedStatus map[uint]string
}

func (f *fakeMeetupOrderRepo) FindByID(_ context.Context, id uint) (*model.TradeOrder, error) {
	if o, ok := f.orders[id]; ok {
		cp := *o
		return &cp, nil
	}
	return nil, util.ErrNotFound
}

// FindByIDForUpdate returns the order with an optional status override,
// simulating a completion that committed between the pre-check and the lock.
func (f *fakeMeetupOrderRepo) FindByIDForUpdate(_ context.Context, id uint) (*model.TradeOrder, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, util.ErrNotFound
	}
	cp := *o
	if s, ok := f.lockedStatus[id]; ok {
		cp.Status = s
	}
	return &cp, nil
}

func (f *fakeMeetupOrderRepo) CompleteByAppointment(_ context.Context, id uint, ts interface{}) error {
	o, ok := f.orders[id]
	if !ok {
		return util.ErrNotFound
	}
	if o.Status != constants.TradeStatusPending && o.Status != constants.TradeStatusConfirmed {
		return util.ErrConflict
	}
	t := ts.(time.Time)
	o.Status = constants.TradeStatusCompleted
	o.CompletedAt = &t
	if o.BuyerConfirmedAt == nil {
		o.BuyerConfirmedAt = &t
	}
	if o.SellerConfirmedAt == nil {
		o.SellerConfirmedAt = &t
	}
	return nil
}

type fakeMeetupProductRepo struct {
	statuses map[uint]string
}

func (f *fakeMeetupProductRepo) UpdateStatus(_ context.Context, id uint, status string) error {
	f.statuses[id] = status
	return nil
}

const (
	meetupBuyerID  = 100
	meetupSellerID = 200
	meetupOrderID  = 1
)

func newMeetupFixture(orderStatus string) (*MeetupAppointmentService, *fakeAppointmentRepo, *fakeMeetupOrderRepo, *fakeMeetupProductRepo) {
	appts := newFakeAppointmentRepo()
	orders := &fakeMeetupOrderRepo{
		orders: map[uint]*model.TradeOrder{
			meetupOrderID: {ID: meetupOrderID, ProductID: 10, BuyerID: meetupBuyerID, SellerID: meetupSellerID, Status: orderStatus},
		},
		lockedStatus: map[uint]string{},
	}
	products := &fakeMeetupProductRepo{statuses: map[uint]string{10: constants.ProductStatusOnSale}}
	svc := NewMeetupAppointmentService(appts, orders, products, slog.Default())
	return svc, appts, orders, products
}

func futureMeetAt() string { return time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04") }
func pastMeetAt() string   { return time.Now().Add(-time.Hour).Format("2006-01-02 15:04") }

func appErrorStatus(err error) int {
	var appErr *util.AppError
	if errors.As(err, &appErr) {
		return appErr.Status
	}
	return 0
}

func TestMeetupAppointmentCreateRules(t *testing.T) {
	tests := []struct {
		name        string
		userID      uint
		orderStatus string
		meetAt      string
		location    string
		wantStatus  int
	}{
		{name: "buyer proposes ok", userID: meetupBuyerID, orderStatus: constants.TradeStatusPending, meetAt: futureMeetAt(), location: "图书馆门口", wantStatus: 0},
		{name: "seller proposes ok", userID: meetupSellerID, orderStatus: constants.TradeStatusConfirmed, meetAt: futureMeetAt(), location: "三食堂", wantStatus: 0},
		{name: "stranger forbidden", userID: 999, orderStatus: constants.TradeStatusPending, meetAt: futureMeetAt(), location: "图书馆门口", wantStatus: 403},
		{name: "completed order rejected", userID: meetupBuyerID, orderStatus: constants.TradeStatusCompleted, meetAt: futureMeetAt(), location: "图书馆门口", wantStatus: 409},
		{name: "cancelled order rejected", userID: meetupBuyerID, orderStatus: constants.TradeStatusCancelled, meetAt: futureMeetAt(), location: "图书馆门口", wantStatus: 409},
		{name: "empty location rejected", userID: meetupBuyerID, orderStatus: constants.TradeStatusPending, meetAt: futureMeetAt(), location: "   ", wantStatus: 400},
		{name: "past time rejected", userID: meetupBuyerID, orderStatus: constants.TradeStatusPending, meetAt: pastMeetAt(), location: "图书馆门口", wantStatus: 400},
		{name: "bad time format rejected", userID: meetupBuyerID, orderStatus: constants.TradeStatusPending, meetAt: "明天下午三点", location: "图书馆门口", wantStatus: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newMeetupFixture(tt.orderStatus)
			appt, err := svc.Create(context.Background(), tt.userID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: tt.meetAt, Location: tt.location})
			if tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				wantCounterpart := uint(meetupSellerID)
				if tt.userID == meetupSellerID {
					wantCounterpart = meetupBuyerID
				}
				if appt.CounterpartID != wantCounterpart || appt.Status != constants.AppointmentStatusPending {
					t.Fatalf("bad appointment: %+v", appt)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got appointment %+v", appt)
			}
			if got := appErrorStatus(err); got != tt.wantStatus {
				t.Fatalf("want http %d, got %d (%v)", tt.wantStatus, got, err)
			}
		})
	}
}

func TestMeetupAppointmentDuplicateRejected(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	req := &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"}
	if _, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, req); err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	if _, err := svc.Create(context.Background(), meetupSellerID, meetupOrderID, req); err == nil {
		t.Fatalf("expected duplicate create to fail")
	} else if got := appErrorStatus(err); got != 409 {
		t.Fatalf("want 409, got %d (%v)", got, err)
	}
}

// TestMeetupAppointmentUniqueIndexConflict simulates the concurrent-create
// race: the pre-check passes but the unique index rejects the insert, which
// must surface as 409 rather than a 500.
func TestMeetupAppointmentUniqueIndexConflict(t *testing.T) {
	svc, appts, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appts.createErr = util.ErrConflict
	_, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if got := appErrorStatus(err); got != 409 {
		t.Fatalf("unique index conflict want 409, got %d (%v)", got, err)
	}
}

// TestMeetupAppointmentCreateOrderCompletedUnderLock simulates the legacy
// seller-confirm committing between the create pre-check and the locked
// re-check: the create must fail and leave no active appointment behind.
func TestMeetupAppointmentCreateOrderCompletedUnderLock(t *testing.T) {
	svc, appts, orders, _ := newMeetupFixture(constants.TradeStatusConfirmed)
	orders.lockedStatus[meetupOrderID] = constants.TradeStatusCompleted
	_, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if got := appErrorStatus(err); got != 409 {
		t.Fatalf("completed-under-lock create want 409, got %d (%v)", got, err)
	}
	if n := len(appts.byID); n != 0 {
		t.Fatalf("expected no appointment rows, got %d", n)
	}
}

func TestMeetupAppointmentRespondRules(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appt, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := svc.Accept(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 403 {
		t.Fatalf("proposer accept want 403, got %d", appErrorStatus(err))
	}
	if _, err := svc.Accept(context.Background(), 999, appt.ID); appErrorStatus(err) != 403 {
		t.Fatalf("stranger accept want 403, got %d", appErrorStatus(err))
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("handover before accept want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.Accept(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("counterpart accept failed: %v", err)
	}
	if _, err := svc.Accept(context.Background(), meetupSellerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("re-accept want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reject(context.Background(), meetupSellerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("reject after accept want 409, got %d", appErrorStatus(err))
	}
}

func TestMeetupAppointmentRejectThenRebook(t *testing.T) {
	svc, _, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appt, _ := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if _, err := svc.Reject(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("reject failed: %v", err)
	}
	if _, err := svc.Create(context.Background(), meetupSellerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "三食堂"}); err != nil {
		t.Fatalf("rebook after reject failed: %v", err)
	}
}

func TestMeetupAppointmentReschedule(t *testing.T) {
	svc, appts, _, _ := newMeetupFixture(constants.TradeStatusPending)
	appt, _ := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if _, err := svc.Reschedule(context.Background(), meetupBuyerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "东门"}); appErrorStatus(err) != 403 {
		t.Fatalf("proposer reschedule want 403, got %d", appErrorStatus(err))
	}
	if _, err := svc.Reschedule(context.Background(), meetupSellerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: pastMeetAt(), Location: "东门"}); appErrorStatus(err) != 400 {
		t.Fatalf("reschedule past time want 400, got %d", appErrorStatus(err))
	}
	fresh, err := svc.Reschedule(context.Background(), meetupSellerID, appt.ID, &dto.RescheduleMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "东门"})
	if err != nil {
		t.Fatalf("reschedule failed: %v", err)
	}
	if fresh.ProposerID != meetupSellerID || fresh.CounterpartID != meetupBuyerID || fresh.Status != constants.AppointmentStatusPending {
		t.Fatalf("bad rescheduled appointment: %+v", fresh)
	}
	old, _ := appts.FindByID(context.Background(), appt.ID)
	if old.Status != constants.AppointmentStatusSuperseded {
		t.Fatalf("old appointment want superseded, got %s", old.Status)
	}
	if _, err := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "西门"}); appErrorStatus(err) != 409 {
		t.Fatalf("create after reschedule want 409, got %d", appErrorStatus(err))
	}
	if _, err := svc.Accept(context.Background(), meetupBuyerID, fresh.ID); err != nil {
		t.Fatalf("accept rescheduled failed: %v", err)
	}
}

func TestMeetupAppointmentHandoverCompletesOrder(t *testing.T) {
	svc, appts, orders, products := newMeetupFixture(constants.TradeStatusPending)
	appt, _ := svc.Create(context.Background(), meetupBuyerID, meetupOrderID, &dto.CreateMeetupAppointmentRequest{MeetAt: futureMeetAt(), Location: "图书馆门口"})
	if _, err := svc.Accept(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("accept failed: %v", err)
	}
	if _, err := svc.HandoverConfirm(context.Background(), 999, appt.ID); appErrorStatus(err) != 403 {
		t.Fatalf("stranger handover want 403, got %d", appErrorStatus(err))
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); err != nil {
		t.Fatalf("buyer handover failed: %v", err)
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupBuyerID, appt.ID); appErrorStatus(err) != 409 {
		t.Fatalf("duplicate buyer handover want 409, got %d", appErrorStatus(err))
	}
	if orders.orders[meetupOrderID].Status == constants.TradeStatusCompleted {
		t.Fatalf("order completed before both confirmations")
	}
	if _, err := svc.HandoverConfirm(context.Background(), meetupSellerID, appt.ID); err != nil {
		t.Fatalf("seller handover failed: %v", err)
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
