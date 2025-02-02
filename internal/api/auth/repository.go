package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Repository interface {
	findUser(ctx context.Context, email string) (*sqlc.AuthUser, error)
	getUserDetails(ctx context.Context, email string) (sqlc.GetUserDetailsRow, error)
	createAdmin(ctx context.Context, user *RegisterRequest) error
	createTenant(ctx context.Context, user *RegisterRequest) error
	createCustomer(ctx context.Context, user *RegisterRequest) error
	updateVerificationStatus(ctx context.Context, userID int64, emailVerified, phoneVerified bool) error
}

type repository struct {
	query *sqlc.Queries
	log   *slog.Logger
}

func NewRepository(pg db.Postgres) Repository {
	l := slog.With(slog.String("layer", "auth-repo"))
	return &repository{query: pg.Queries(), log: l}
}

func (r *repository) findUser(ctx context.Context, email string) (*sqlc.AuthUser, error) {
	user, err := r.query.GetUser(ctx, email)

	if err != nil {
		r.log.Error(err.Error())

		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found: %s", email)
		}
		return nil, errors.New("unknown error ocurred")
	}

	return &user, nil
}

func (r *repository) getUserDetails(ctx context.Context, email string) (sqlc.GetUserDetailsRow, error) {
	user, err := r.query.GetUserDetails(ctx, email)
	user.Password = ""
	if err != nil {
		return user, fmt.Errorf("failed to get user: %w", err)
	}

	return user, nil
}

func (r *repository) createAdmin(ctx context.Context, user *RegisterRequest) error {
	_, err := r.query.CreateAdminUser(ctx, sqlc.CreateAdminUserParams{
		Email:    user.Email,
		Phone:    user.Phone,
		Password: user.Password,
	})

	if err != nil {
		r.log.Error(err.Error())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return fmt.Errorf("admin already registered: %s , %s", user.Email, user.Phone)
		}
		return fmt.Errorf("failed to register admin: %w", err)
	}

	return nil
}

func (r *repository) createTenant(ctx context.Context, user *RegisterRequest) error {
	_, err := r.query.CreateTenantUser(ctx, sqlc.CreateTenantUserParams{
		Email:       user.Email,
		Phone:       user.Phone,
		Password:    user.Password,
		Fullname:    user.FullName,
		Domain:      user.Domain,
		Companyname: user.CompanyName,
	})

	if err != nil {
		r.log.Error(err.Error())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return fmt.Errorf("tenant already registered: %s , %s", user.Email, user.Phone)
		}
		return fmt.Errorf("failed to register tenant: %w", err)
	}

	return nil
}

func (r *repository) createCustomer(ctx context.Context, user *RegisterRequest) error {
	_, err := r.query.CreateCustomerUser(ctx, sqlc.CreateCustomerUserParams{
		Email:     user.Email,
		Phone:     user.Phone,
		Password:  user.Password,
		Firstname: user.FirstName,
		Lastname:  user.LastName,
	})

	if err != nil {
		r.log.Error(err.Error())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return fmt.Errorf("customer already registered: %s , %s", user.Email, user.Phone)
		}
		return fmt.Errorf("failed to register customer: %w", err)
	}

	return nil
}

func (r *repository) updateVerificationStatus(ctx context.Context, userID int64, emailVerified, phoneVerified bool) error {
	rows, err := r.query.UpdateVerification(ctx, sqlc.UpdateVerificationParams{
		Userid:        userID,
		Emailverified: emailVerified,
		Phoneverified: phoneVerified,
	})

	if err != nil {
		r.log.Error(err.Error())
		return err
	}

	if rows == 0 {
		return errors.New("user not found")
	}

	return nil
}
