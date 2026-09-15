package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/repository"
	"github.com/lp/campus-market/internal/util"
)

// TradeOrderService manages purchase intents, confirmations and completion.
type TradeOrderService struct {
	orders       *repository.TradeOrderRepository
	products     *repository.ProductRepository
	appointments *repository.MeetupAppointmentRepository
	logger       *slog.Logger
}

// NewTradeOrderService wires the trade order service dependencies.
func NewTradeOrderService(orders *repository.TradeOrderRepository, products *repository.ProductRepository, appointments *repository.MeetupAppointmentRepository, logger *slog.Logger) *TradeOrderService {
	return &TradeOrderService{orders: orders, products: products, appointments: appointments, logger: logger}
}

// Create creates a pending trade order for an on-sale product.
func (s *TradeOrderService) Create(ctx context.Context, buyer *model.User, req *dto.CreateTradeOrderRequest) (*model.TradeOrder, error) {
	product, err := s.products.FindByID(ctx, req.ProductID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[buyer=%d] product lookup: %w", buyer.ID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if product.SellerID == buyer.ID {
		return nil, util.NewAppError(400, constants.CodeBadRequest, "不能购买自己的商品", nil)
	}
	if product.Status != constants.ProductStatusOnSale {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgProductNotOnSale, nil)
	}
	if existing, err := s.orders.FindByProductAndBuyer(ctx, req.ProductID, buyer.ID); err == nil && existing != nil {
		return nil, util.NewAppError(409, constants.CodeConflict, "您已对该商品下单", nil)
	}
	order := &model.TradeOrder{
		ProductID: req.ProductID, BuyerID: buyer.ID, SellerID: product.SellerID,
		Status: constants.TradeStatusPending,
	}
	if err := s.orders.Create(ctx, order); err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[buyer=%d] create: %w", buyer.ID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTradeOrderCreateSuccess, order.ID, req.ProductID))
	return order, nil
}

// ListMy returns the orders where the user participates, each enriched with
// its latest meetup appointment for the trade list display.
func (s *TradeOrderService) ListMy(ctx context.Context, userID uint, q *dto.PageQuery) (*dto.PageResult, error) {
	q.Normalize()
	items, total, err := s.orders.ListByUser(ctx, userID, q.Page, q.PageSize)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[user=%d] list: %w", userID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	ids := make([]uint, 0, len(items))
	for _, o := range items {
		ids = append(ids, o.ID)
	}
	appointments, err := s.appointments.ListLatestByOrders(ctx, ids)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[user=%d] list appointments: %w", userID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	views := make([]dto.TradeOrderView, 0, len(items))
	for _, o := range items {
		view := dto.TradeOrderView{TradeOrder: o}
		if appt, ok := appointments[o.ID]; ok {
			view.Appointment = appt
		}
		views = append(views, view)
	}
	return &dto.PageResult{Items: views, Total: total, Page: q.Page, PageSize: q.PageSize}, nil
}

// ensureNoActiveAppointment blocks the legacy confirm entries while the order
// has a valid (pending/accepted) meetup appointment: the order may then only
// be completed through the appointment's dual handover confirmation.
func (s *TradeOrderService) ensureNoActiveAppointment(ctx context.Context, orderID, userID uint) error {
	if _, err := s.appointments.FindActiveByOrder(ctx, orderID); err == nil {
		s.logger.Warn(fmt.Sprintf(constants.LogTradeOrderConfirmBlocked, orderID, userID))
		return util.NewAppError(409, constants.CodeConflict, constants.MsgConfirmViaHandover, nil)
	} else if !errors.Is(err, util.ErrNotFound) {
		return util.WrapAppError(fmt.Errorf("trade_order[id=%d] active appointment lookup: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	return nil
}

// BuyerConfirm marks the order confirmed by the buyer.
func (s *TradeOrderService) BuyerConfirm(ctx context.Context, userID, orderID uint) (*model.TradeOrder, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] buyer confirm find: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if order.BuyerID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	if order.Status != constants.TradeStatusPending {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgTradeStatusInvalid, nil)
	}
	if err := s.ensureNoActiveAppointment(ctx, orderID, userID); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.orders.UpdateBuyerConfirmed(ctx, orderID, now); err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] buyer confirm: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTradeOrderBuyerConfirmSuccess, orderID))
	order.Status = constants.TradeStatusConfirmed
	return order, nil
}

// SellerConfirm completes the order and marks the product sold. The active
// appointment check runs twice: once as a fast path, and again inside the
// transaction after the order row lock is taken — so a concurrently created
// appointment either blocks this completion or is itself rejected by the
// locked status re-check on the create side. Never both succeed.
func (s *TradeOrderService) SellerConfirm(ctx context.Context, userID, orderID uint) (*model.TradeOrder, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] seller confirm find: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if order.SellerID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	if order.Status != constants.TradeStatusConfirmed {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgTradeStatusInvalid, nil)
	}
	if err := s.ensureNoActiveAppointment(ctx, orderID, userID); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := s.orders.Transaction(ctx, func(txCtx context.Context) error {
		if err := s.orders.UpdateSellerConfirmed(txCtx, orderID, now); err != nil {
			return err
		}
		// 行锁保护下复查：若有预约在本事务等待期间落库，则放弃完成。
		if _, err := s.appointments.FindActiveByOrder(txCtx, orderID); err == nil {
			s.logger.Warn(fmt.Sprintf(constants.LogTradeOrderConfirmBlocked, orderID, userID))
			return util.NewAppError(409, constants.CodeConflict, constants.MsgConfirmViaHandover, nil)
		} else if !errors.Is(err, util.ErrNotFound) {
			return err
		}
		if err := s.products.UpdateStatus(txCtx, order.ProductID, constants.ProductStatusSold); err != nil {
			return err
		}
		return nil
	}); err != nil {
		var appErr *util.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgTradeStatusInvalid, nil)
		}
		s.logger.Error(fmt.Sprintf(constants.LogTradeOrderCompleteFailed, orderID, err))
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] seller confirm: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTradeOrderCompleteSuccess, orderID, order.ProductID))
	order.Status = constants.TradeStatusCompleted
	return order, nil
}

// Cancel cancels a pending order. The guarded update and the voiding of any
// active meetup appointments run in one transaction, so a successful cancel
// never leaves a pending/accepted appointment behind — regardless of whether
// an appointment was created or accepted concurrently.
func (s *TradeOrderService) Cancel(ctx context.Context, userID, orderID uint) (*model.TradeOrder, error) {
	order, err := s.orders.FindByID(ctx, orderID)
	if err != nil {
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] cancel find: %w", orderID, err), 404, constants.CodeNotFound, constants.MsgNotFound)
	}
	if order.BuyerID != userID && order.SellerID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden, constants.MsgNotParticipant, nil)
	}
	if order.Status != constants.TradeStatusPending {
		return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgTradeStatusInvalid, nil)
	}
	now := time.Now()
	var voided int64
	if err := s.orders.Transaction(ctx, func(txCtx context.Context) error {
		if err := s.orders.CancelIfPending(txCtx, orderID); err != nil {
			return err
		}
		n, err := s.appointments.CancelActiveByOrder(txCtx, orderID, now)
		if err != nil {
			return err
		}
		voided = n
		return nil
	}); err != nil {
		if errors.Is(err, util.ErrConflict) {
			return nil, util.NewAppError(409, constants.CodeConflict, constants.MsgTradeStatusInvalid, nil)
		}
		return nil, util.WrapAppError(fmt.Errorf("trade_order[id=%d] cancel: %w", orderID, err), 500, constants.CodeInternalError, constants.MsgInternalError)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTradeOrderCancelSuccess, orderID))
	if voided > 0 {
		s.logger.Info(fmt.Sprintf(constants.LogAppointmentsVoidedByCancel, orderID, voided))
	}
	order.Status = constants.TradeStatusCancelled
	return order, nil
}
