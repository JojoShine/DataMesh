package identity

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) CreateUser(ctx context.Context, user User, platforms, roles []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := EnsureLegacyUnit(tx, user.DepartmentID); err != nil {
			return err
		}
		unit, err := ReadAffiliation(tx, user.DepartmentID)
		if err != nil {
			return err
		}
		if !unit.Active {
			return ErrInput
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		for _, platform := range platforms {
			if err := tx.Create(&PlatformAccess{UserID: user.ID, Platform: platform}).Error; err != nil {
				return err
			}
		}
		for _, role := range roles {
			if err := tx.Create(&UserRole{UserID: user.ID, Role: role}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&AuditRecord{Action: "create_user", ObjectID: user.ID, Result: "success", TraceID: user.ID}).Error
	})
}

func (r *Repository) FindUser(ctx context.Context, username string) (User, error) {
	var user User
	err := r.db.WithContext(ctx).Where("username = ?", username).Take(&user).Error
	return user, err
}

func (r *Repository) CreateSession(ctx context.Context, session Session, previousHash, traceID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var active User
		// Serialize session issuance with disabling so no session can appear after its cleanup.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND active = true", session.UserID).Take(&active).Error; err != nil {
			return err
		}
		if previousHash != "" {
			if err := tx.Where("token_hash = ?", previousHash).Delete(&Session{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		return tx.Create(&AuditRecord{ActorID: &session.UserID, Action: "login", ObjectID: session.UserID, Result: "success", TraceID: traceID}).Error
	})
}

func (r *Repository) Principal(ctx context.Context, hash string, idle time.Duration) (Principal, error) {
	var principal Principal
	result := r.db.WithContext(ctx).Raw(`SELECT u.id AS user_id, u.username, u.display_name, u.department_id, u.public_test
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND u.active = true AND s.expires_at > now()
		AND s.last_seen_at > now() - (? * interval '1 second')`, hash, int64(idle.Seconds())).Scan(&principal)
	if result.Error != nil {
		return Principal{}, result.Error
	}
	if principal.UserID == "" {
		return Principal{}, gorm.ErrRecordNotFound
	}
	principal.Platforms = []string{}
	principal.Roles = []string{}
	if err := r.db.WithContext(ctx).Model(&UserRole{}).Where("user_id = ?", principal.UserID).Order("role").Pluck("role", &principal.Roles).Error; err != nil {
		return Principal{}, err
	}
	if err := r.db.WithContext(ctx).Model(&PlatformAccess{}).Where("user_id = ?", principal.UserID).Order("platform").Pluck("platform", &principal.Platforms).Error; err != nil {
		return Principal{}, err
	}
	if err := r.db.WithContext(ctx).Model(&Session{}).Where("token_hash = ?", hash).UpdateColumn("last_seen_at", gorm.Expr("GREATEST(last_seen_at, now())")).Error; err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func (r *Repository) Logout(ctx context.Context, hash, userID, traceID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token_hash = ?", hash).Delete(&Session{}).Error; err != nil {
			return err
		}
		return tx.Create(&AuditRecord{ActorID: &userID, Action: "logout", ObjectID: userID, Result: "success", TraceID: traceID}).Error
	})
}

func (r *Repository) AuditFailure(ctx context.Context, traceID string) error {
	return r.db.WithContext(ctx).Create(&AuditRecord{Action: "login", ObjectID: "account", Result: "denied", TraceID: traceID}).Error
}

func (r *Repository) DisableUser(ctx context.Context, username string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.Where("username = ?", username).Take(&user).Error; err != nil {
			return err
		}
		if err := tx.Model(&user).Update("active", false).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&Session{}).Error; err != nil {
			return err
		}
		return tx.Create(&AuditRecord{Action: "disable_user", ObjectID: user.ID, Result: "success", TraceID: user.ID}).Error
	})
}

func IsMissing(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
