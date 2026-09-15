package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/model"
	"github.com/lp/campus-market/internal/util"
	"gorm.io/gorm"
)

// MeetupActiveOrderKeyColumn is the generated column backing the "one active
// appointment per order" unique index. It equals order_id while the row is
// pending/accepted and NULL otherwise, so only active rows collide.
const MeetupActiveOrderKeyColumn = "active_order_key"

// EnsureMeetupConstraints adds the generated column and unique index that
// guarantee a single active appointment per order. AutoMigrate cannot express
// generated columns, so existing databases are patched here idempotently.
func EnsureMeetupConstraints(db *gorm.DB) error {
	var count int64
	err := db.Raw(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'meetup_appointments' AND COLUMN_NAME = ?`,
		MeetupActiveOrderKeyColumn).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check meetup active order key column: %w", err)
	}
	if count > 0 {
		return nil
	}
	if err := db.Exec(`ALTER TABLE meetup_appointments
		ADD COLUMN active_order_key BIGINT UNSIGNED GENERATED ALWAYS AS (
			CASE WHEN status IN ('pending','accepted') THEN order_id ELSE NULL END
		) STORED,
		ADD UNIQUE KEY uq_meetup_active_order (active_order_key)`).Error; err != nil {
		return fmt.Errorf("add meetup active order key constraint: %w", err)
	}
	return nil
}

// MeetupAppointmentRepository persists meetup appointment rows.
type MeetupAppointmentRepository struct {
	db *gorm.DB
}

// NewMeetupAppointmentRepository builds a MeetupAppointmentRepository.
func NewMeetupAppointmentRepository(db *gorm.DB) *MeetupAppointmentRepository {
	return &MeetupAppointmentRepository{db: db}
}

// Transaction runs fn inside a database transaction for cross-repository writes.
func (r *MeetupAppointmentRepository) Transaction(ctx context.Context, fn func(txCtx context.Context) error) error {
	return Transaction(ctx, r.db, fn)
}

// Create inserts a new meetup appointment. A duplicate active appointment for
// the same order trips the unique index and is reported as util.ErrConflict.
func (r *MeetupAppointmentRepository) Create(ctx context.Context, a *model.MeetupAppointment) error {
	err := db(ctx, r.db).Create(a).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return util.ErrConflict
	}
	return err
}

// FindByID returns a meetup appointment by id.
func (r *MeetupAppointmentRepository) FindByID(ctx context.Context, id uint) (*model.MeetupAppointment, error) {
	var a model.MeetupAppointment
	err := db(ctx, r.db).First(&a, id).Error
	if err != nil {
		return nil, normalizeError(err)
	}
	return &a, nil
}

// FindActiveByOrder returns the single valid (pending/accepted) appointment of
// an order, or util.ErrNotFound when none exists.
func (r *MeetupAppointmentRepository) FindActiveByOrder(ctx context.Context, orderID uint) (*model.MeetupAppointment, error) {
	var a model.MeetupAppointment
	err := db(ctx, r.db).
		Where("order_id = ? AND status IN ?", orderID, constants.AppointmentActiveStatuses).
		First(&a).Error
	if err != nil {
		return nil, normalizeError(err)
	}
	return &a, nil
}

// ListByOrder returns the full appointment history of an order, newest first.
func (r *MeetupAppointmentRepository) ListByOrder(ctx context.Context, orderID uint) ([]model.MeetupAppointment, error) {
	var items []model.MeetupAppointment
	err := db(ctx, r.db).Where("order_id = ?", orderID).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListLatestByOrders returns the latest appointment per order for list display.
func (r *MeetupAppointmentRepository) ListLatestByOrders(ctx context.Context, orderIDs []uint) (map[uint]*model.MeetupAppointment, error) {
	out := make(map[uint]*model.MeetupAppointment, len(orderIDs))
	if len(orderIDs) == 0 {
		return out, nil
	}
	var rows []model.MeetupAppointment
	if err := db(ctx, r.db).Where("order_id IN ?", orderIDs).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].OrderID] = &rows[i]
	}
	return out, nil
}

// CancelActiveByOrder voids every active (pending/accepted) appointment of an
// order and returns how many were voided. It runs inside the order-cancel
// transaction so a cancelled order never retains a valid appointment.
func (r *MeetupAppointmentRepository) CancelActiveByOrder(ctx context.Context, orderID uint, ts time.Time) (int64, error) {
	res := db(ctx, r.db).Model(&model.MeetupAppointment{}).
		Where("order_id = ? AND status IN ?", orderID, constants.AppointmentActiveStatuses).
		Updates(map[string]interface{}{"status": constants.AppointmentStatusCancelled, "responded_at": ts})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// UpdateStatus moves an appointment to a new status when its current status is
// in from, stamping responded_at. It returns util.ErrConflict on state races.
func (r *MeetupAppointmentRepository) UpdateStatus(ctx context.Context, id uint, from []string, to string, respondedAt time.Time) error {
	res := db(ctx, r.db).Model(&model.MeetupAppointment{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(map[string]interface{}{"status": to, "responded_at": respondedAt})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return util.ErrConflict
	}
	return nil
}

// ConfirmHandover stamps one party's handover confirmation on an accepted
// appointment. column is service-controlled and never user input.
func (r *MeetupAppointmentRepository) ConfirmHandover(ctx context.Context, id uint, column string, ts time.Time) error {
	res := db(ctx, r.db).Model(&model.MeetupAppointment{}).
		Where("id = ? AND status = ?", id, constants.AppointmentStatusAccepted).
		Where(column + " IS NULL").
		Update(column, ts)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return util.ErrConflict
	}
	return nil
}

// MarkCompleted finishes an accepted appointment once both parties confirmed.
func (r *MeetupAppointmentRepository) MarkCompleted(ctx context.Context, id uint) error {
	res := db(ctx, r.db).Model(&model.MeetupAppointment{}).
		Where("id = ? AND status = ?", id, constants.AppointmentStatusAccepted).
		Update("status", constants.AppointmentStatusCompleted)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return util.ErrConflict
	}
	return nil
}
