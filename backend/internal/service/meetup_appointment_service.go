package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
)

// MeetupAppointmentRepository is the data access contract for meetup appointments.
type MeetupAppointmentRepository interface {
	Create(ctx context.Context, a *model.MeetupAppointment) error
	FindByID(ctx context.Context, id uint) (*model.MeetupAppointment, error)
	FindActiveByOrder(ctx context.Context, orderID uint) (*model.MeetupAppointment, error)
	ListByOrder(ctx context.Context, orderID uint) ([]model.MeetupAppointment, error)
	UpdateStatus(ctx context.Context, id uint, from []string, to string, respondedAt time.Time) error
	ConfirmHandover(ctx context.Context, id uint, column string, ts time.Time) error
	MarkCompleted(ctx context.Context, id uint) error
	Transaction(ctx context.Context, fn func(txCtx context.Context) error) error
}

// MeetupOrderRepository is the trade order contract the appointment flow needs.
type MeetupOrderRepository interface {
	FindByID(ctx context.Context, id uint) (*model.TradeOrder, error)
	FindByIDForUpdate(ctx context.Context, id uint) (*model.TradeOrder, error)
	CompleteByAppointment(ctx context.Context, id uint, ts interface{}) error
}

// MeetupProductRepository is the product contract the appointment flow needs.
type MeetupProductRepository interface {
	UpdateStatus(ctx context.Context, id uint, status string) error
}

// MeetupAppointmentService manages face-to-face handover appointments:
// propose, accept, reject, reschedule and dual handover confirmation.
type MeetupAppointmentService struct {
	appointments MeetupAppointmentRepository
	orders       MeetupOrderRepository
	products     MeetupProductRepository
	logger       *slog.Logger
}

// NewMeetupAppointmentService wires the meetup appointment service dependencies.
func NewMeetupAppointmentService(appointments MeetupAppointmentRepository, orders MeetupOrderRepository, products MeetupProductRepository, logger *slog.Logger) *MeetupAppointmentService {
	return &MeetupAppointmentService{appointments: appointments, orders: orders, products: products, logger: logger}
}

// meetAtLayouts are the accepted meet_at input layouts, local time.
var meetAtLayouts = []string{"2006-01-02 15:04", "2006-01-02 15:04:05", time.RFC3339}

// parseMeetAt parses the requested meet time against the supported layouts.
func parseMeetAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range meetAtLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported meet_at format: %q", raw)
}

// validateProposal checks the shared create/reschedule rules: non-empty
// on-campus location and a meet time strictly in the future.
func validateProposal(meetAtRaw, location string) (time.Time, string, error) {
	loc := strings.TrimSpace(location)
	if loc == "" {
		return time.Time{}, "", util.NewAppError(400, constants.CodeValidation, constants.MsgLocationRequired, nil)
	}
	meetAt, err := parseMeetAt(meetAtRaw)
	if err != nil {
		return time.Time{}, "", util.NewAppError(400, constants.CodeValidation, "面交时间格式不正确", err)
	}
	if !meetAt.After(time.Now()) {
		return time.Time{}, "", util.NewAppError(400, constants.CodeValidation, constants.MsgAppointmentTimePast, nil)
	}
	return meetAt, loc, nil
}

// orderActiveForAppointment reports whether an order may hold an appointment.
func orderActiveForAppointment(status string) bool {
	return status == constants.TradeStatusPending || status == constants.TradeStatusConfirmed
}

// loadWithOrder fetches the appointment and its parent order with 404 mapping.
func (s *MeetupAppointmentService) loadWithOrder(ctx context.Context, appointmentID uint) (*model.MeetupAppointment, *model.TradeOrder, error) {
	appt, err := s.appointments.FindByID(ctx, appointmentID)
	if err != nil {
		return nil, nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] find: %w", appointmentID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	order, err := s.orders.FindByID(ctx, appt.OrderID)
	if err != nil {
		return nil, nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] order lookup: %w", appointmentID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	return appt, order, nil
}

// Create proposes a face-to-face appointment on an unfinished order. The
// status re-check and the insert run inside a transaction that holds a lock on
// the order row, so a concurrent legacy completion (seller-confirm) either
// commits first (create then sees the completed order and fails) or waits for
// the insert (its own guarded completion then fails) — never both.
func (s *MeetupAppointmentService) Create(ctx context.Context, userID, orderID uint, req *dto.CreateMeetupAppointmentRequest) (*model.MeetupAppointment, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] create find: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if order.BuyerID != userID && order.SellerID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	meetAt, location, err := validateProposal(req.MeetAt, req.Location)
	if err != nil {
		s.logger.Warn(fmt.Sprintf(constants.LogAppointmentCreateRejected, orderID, userID, err))
		return nil, err
	}
	if _, err := s.appointments.FindActiveByOrder(ctx, orderID); err == nil {
		s.logger.Warn(fmt.Sprintf(constants.LogAppointmentCreateRejected, orderID, userID, "active appointment exists"))
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentExists, nil)
	} else if !errors.Is(err, util.ErrNotFound) {
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] active lookup: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	counterpartID := order.SellerID
	if userID == order.SellerID {
		counterpartID = order.BuyerID
	}
	appt := &model.MeetupAppointment{
		OrderID: order.ID, ProposerID: userID, CounterpartID: counterpartID,
		MeetAt: meetAt, Location: location, Status: constants.AppointmentStatusPending,
	}
	err = s.appointments.Transaction(ctx, func(txCtx context.Context) error {
		locked, err := s.orders.FindByIDForUpdate(txCtx, orderID)
		if err != nil {
			return err
		}
		if !orderActiveForAppointment(locked.Status) {
			s.logger.Warn(fmt.Sprintf(constants.LogAppointmentCreateRejected, orderID, userID, "order status="+locked.Status))
			return util.NewAppError(409, constants.CodeConflict, constants.MsgOrderNotActive, nil)
		}
		if _, err := s.appointments.FindActiveByOrder(txCtx, orderID); err == nil {
			s.logger.Warn(fmt.Sprintf(constants.LogAppointmentCreateRejected, orderID, userID, "active appointment exists"))
			return util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentExists, nil)
		} else if !errors.Is(err, util.ErrNotFound) {
			return err
		}
		return s.appointments.Create(txCtx, appt)
	})
	if err != nil {
		var appErr *util.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		if errors.Is(err, util.ErrConflict) {
			// 并发下唯一索引兜底：另一份进行中的预约已抢先落库。
			s.logger.Warn(fmt.Sprintf(constants.LogAppointmentCreateRejected, orderID, userID, "active appointment exists (unique index)"))
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentExists, nil)
		}
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] create lock: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
		}
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] create: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppointmentCreateSuccess, appt.ID, order.ID, userID))
	return appt, nil
}

// ListByOrder returns the appointment history of an order for participants.
func (s *MeetupAppointmentService) ListByOrder(ctx context.Context, userID, orderID uint) ([]model.MeetupAppointment, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] list find: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if order.BuyerID != userID && order.SellerID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	items, err := s.appointments.ListByOrder(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[order=%d] list: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	return items, nil
}

// respond runs the shared guards for accept/reject/reschedule: only the
// counterpart of a still-pending appointment on an active order may respond.
func (s *MeetupAppointmentService) respondable(ctx context.Context, userID, appointmentID uint) (*model.MeetupAppointment, *model.TradeOrder, error) {
	appt, order, err := s.loadWithOrder(ctx, appointmentID)
	if err != nil {
		return nil, nil, err
	}
	if appt.CounterpartID != userID {
		return nil, nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotCounterpart, nil)
	}
	if appt.Status != constants.AppointmentStatusPending {
		return nil, nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentInvalid, nil)
	}
	if !orderActiveForAppointment(order.Status) {
		return nil, nil, util.NewAppError(409, constants.CodeConflict, constants.MsgOrderNotActive, nil)
	}
	return appt, order, nil
}

// Accept accepts a pending appointment; only the counterpart may accept.
func (s *MeetupAppointmentService) Accept(ctx context.Context, userID, appointmentID uint) (*model.MeetupAppointment, error) {
	appt, _, err := s.respondable(ctx, userID, appointmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.appointments.UpdateStatus(ctx, appt.ID, []string{constants.AppointmentStatusPending}, constants.AppointmentStatusAccepted, now); err != nil {
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentInvalid, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] accept: %w", appt.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppointmentAcceptSuccess, appt.ID, userID))
	appt.Status = constants.AppointmentStatusAccepted
	appt.RespondedAt = &now
	return appt, nil
}

// Reject rejects a pending appointment; only the counterpart may reject.
func (s *MeetupAppointmentService) Reject(ctx context.Context, userID, appointmentID uint) (*model.MeetupAppointment, error) {
	appt, _, err := s.respondable(ctx, userID, appointmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.appointments.UpdateStatus(ctx, appt.ID, []string{constants.AppointmentStatusPending}, constants.AppointmentStatusRejected, now); err != nil {
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentInvalid, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] reject: %w", appt.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppointmentRejectSuccess, appt.ID, userID))
	appt.Status = constants.AppointmentStatusRejected
	appt.RespondedAt = &now
	return appt, nil
}

// Reschedule supersedes a pending appointment with a new proposal from the
// counterpart, keeping exactly one valid appointment per order.
func (s *MeetupAppointmentService) Reschedule(ctx context.Context, userID, appointmentID uint, req *dto.RescheduleMeetupAppointmentRequest) (*model.MeetupAppointment, error) {
	appt, order, err := s.respondable(ctx, userID, appointmentID)
	if err != nil {
		return nil, err
	}
	meetAt, location, err := validateProposal(req.MeetAt, req.Location)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	fresh := &model.MeetupAppointment{
		OrderID: order.ID, ProposerID: userID, CounterpartID: appt.ProposerID,
		MeetAt: meetAt, Location: location, Status: constants.AppointmentStatusPending,
	}
	err = s.appointments.Transaction(ctx, func(txCtx context.Context) error {
		if err := s.appointments.UpdateStatus(txCtx, appt.ID, []string{constants.AppointmentStatusPending}, constants.AppointmentStatusSuperseded, now); err != nil {
			return err
		}
		return s.appointments.Create(txCtx, fresh)
	})
	if err != nil {
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgAppointmentInvalid, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] reschedule: %w", appt.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppointmentRescheduleSuccess, appt.ID, fresh.ID, userID))
	return fresh, nil
}

// HandoverConfirm stamps one party's physical handover confirmation on an
// accepted appointment; when both parties confirmed, the order completes and
// the product is marked sold atomically. The order row is locked and its
// status re-checked inside the transaction (same lock order as create:
// order → appointment), so a concurrently cancelled or completed order makes
// the confirmation fail instead of leaving inconsistent state.
func (s *MeetupAppointmentService) HandoverConfirm(ctx context.Context, userID, appointmentID uint) (*model.MeetupAppointment, error) {
	appt, order, err := s.loadWithOrder(ctx, appointmentID)
	if err != nil {
		return nil, err
	}
	var column, role string
	switch userID {
	case order.BuyerID:
		column, role = "buyer_confirmed_at", "buyer"
	case order.SellerID:
		column, role = "seller_confirmed_at", "seller"
	default:
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	if !orderActiveForAppointment(order.Status) {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgOrderNotActive, nil)
	}
	if appt.Status != constants.AppointmentStatusAccepted {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgHandoverNotAccepted, nil)
	}
	now := time.Now()
	completed := false
	err = s.appointments.Transaction(ctx, func(txCtx context.Context) error {
		locked, err := s.orders.FindByIDForUpdate(txCtx, order.ID)
		if err != nil {
			return err
		}
		if !orderActiveForAppointment(locked.Status) {
			return util.NewAppError(409, constants.CodeConflict, constants.MsgOrderNotActive, nil)
		}
		if err := s.appointments.ConfirmHandover(txCtx, appt.ID, column, now); err != nil {
			return err
		}
		fresh, err := s.appointments.FindByID(txCtx, appt.ID)
		if err != nil {
			return err
		}
		if fresh.BuyerConfirmedAt == nil || fresh.SellerConfirmedAt == nil {
			return nil
		}
		if err := s.appointments.MarkCompleted(txCtx, appt.ID); err != nil {
			return err
		}
		if err := s.orders.CompleteByAppointment(txCtx, order.ID, now); err != nil {
			return err
		}
		if err := s.products.UpdateStatus(txCtx, order.ProductID, constants.ProductStatusSold); err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err != nil {
		var appErr *util.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgHandoverConfirmed, nil)
		}
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] handover lock: %w", appt.ID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppointmentCompleteFailed, appt.ID, err))
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] handover confirm: %w", appt.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppointmentHandoverSuccess, appt.ID, userID, role))
	if completed {
		s.logger.Info(fmt.Sprintf(constants.LogAppointmentCompleteSuccess, appt.ID, order.ID, order.ProductID))
	}
	fresh, err := s.appointments.FindByID(ctx, appt.ID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("meetup_appointment[id=%d] reload: %w", appt.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	return fresh, nil
}
