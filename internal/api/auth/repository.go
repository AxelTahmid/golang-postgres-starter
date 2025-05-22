package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

type Repository interface {
	FindUser(ctx context.Context, email string) (*sqlc.AuthUser, error)
	GetUserDetails(ctx context.Context, email string) (sqlc.AuthUser, error)
	CreateAdmin(ctx context.Context, user *RegisterRequest) error
	UpdateVerificationStatus(ctx context.Context, userID int64, emailVerified, phoneVerified bool) error
}

type repository struct {
	query *sqlc.Queries
}

func NewRepository(pg db.DB) Repository {
	return &repository{
		query: pg.RootStore().Queries(),
	}
}

func (r *repository) FindUser(ctx context.Context, identity string) (*sqlc.AuthUser, error) {
	user, err := r.query.GetUser(ctx, identity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found: %s", identity)
		}
		return nil, errors.New("unknown error ocurred")
	}

	return &user, nil
}

func (r *repository) GetUserDetails(ctx context.Context, identity string) (sqlc.AuthUser, error) {
	user, err := r.query.GetUserDetails(ctx, identity)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.AuthUser{}, fmt.Errorf("user not found: %s", identity)
		}

		return sqlc.AuthUser{}, errors.New("unknown error ocurred")
	}

	return user, nil
}

func (r *repository) CreateAdmin(ctx context.Context, user *RegisterRequest) error {
	return r.query.CreateAdminUser(ctx, sqlc.CreateAdminUserParams{
		Email:    user.Email,
		Password: user.Password,
	})
}

func (r *repository) UpdateVerificationStatus(
	ctx context.Context,
	userID int64,
	emailVerified, phoneVerified bool,
) error {
	rows, err := r.query.UpdateVerification(ctx, sqlc.UpdateVerificationParams{
		Userid:        userID,
		Emailverified: emailVerified,
		Phoneverified: phoneVerified,
	})
	if err != nil {
		return err
	}

	if rows == 0 {
		return errors.New("user not found")
	}

	return nil
}
