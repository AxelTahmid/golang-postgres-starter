package auth

import (
	"context"
	"errors"

	"github.com/AxelTahmid/tinker/internal/db"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/AxelTahmid/tinker/internal/jwt"
	"github.com/AxelTahmid/tinker/internal/middleware"
	"github.com/AxelTahmid/tinker/internal/utils/argon2id"
)

type Service interface {
	Register(ctx context.Context, user *RegisterRequest) error
	RegisterAdmin(ctx context.Context, user *RegisterRequest) error
	Login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error)
	Me(ctx context.Context, email string) (sqlc.GetUserDetailsRow, error)
	Refresh(ctx context.Context, refreshToken string) (*jwt.Tokens, error)
}

type service struct {
	repo  Repository
	token jwt.Service
	argon *argon2id.Config
}

func NewService(repo Repository) Service {
	return &service{
		repo:  repo,
		token: jwt.GetService(),
		argon: argon2id.DefaultConfig(),
	}
}

func (s *service) Register(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := s.argon.HashPassword(user.Password)
	if err != nil {
		return err
	}

	user.Password = hashedPassword

	tenantID := ctx.Value(middleware.TenantID)
	if tenantID == nil {
		return errors.New("customer register attempted with unknown tenant")
	}

	if err = s.repo.CreateCustomer(ctx, user); err != nil {
		return db.WrapDBError(ctx, err)
	}

	return nil
}

func (s *service) RegisterTenant(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := s.argon.HashPassword(user.Password)
	if err != nil {
		return err
	}

	user.Password = hashedPassword

	_, err = s.repo.CreateTenant(ctx, user)
	if err != nil {
		return db.WrapDBError(ctx, err)
	}

	// TODO: do this after onboarding step.
	// queue, queueErr := db.GetRiverClient()
	// if queueErr == nil {
	// 	hCustomer := helcim.Customer{
	// 		ID:           int(tenantID),
	// 		CustomerCode: "TRT00" + strconv.Itoa(int(tenantID)),
	// 		BusinessName: user.CompanyName,
	// 		CellPhone:    user.Phone,
	// 	}

	// 	_, _ = queue.Insert(ctx, db.HelcimCustomerArgs{
	// 		Customer: hCustomer,
	// 		Action:   db.ActionCustomerCreate,
	// 		APIToken: "admin",
	// 	}, &river.InsertOpts{
	// 		MaxAttempts: 3,
	// 		Queue:       db.QueueHelcim,
	// 		Tags:        []string{"Tenant", "Registration"},
	// 	})
	// }

	return nil
}

func (s *service) RegisterAdmin(ctx context.Context, user *RegisterRequest) error {
	hashedPassword, err := s.argon.HashPassword(user.Password)
	if err != nil {
		return err
	}

	user.Password = hashedPassword

	if err = s.repo.CreateAdmin(ctx, user); err != nil {
		return db.WrapDBError(ctx, err)
	}

	return nil
}

func (s *service) Login(ctx context.Context, credentials *LoginRequest) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, credentials.Identity)
	if err != nil {
		return nil, db.WrapDBError(ctx, err)
	}

	match, err := s.argon.ComparePasswordAndHash(credentials.Password, user.Password)
	if err != nil {
		return nil, err
	}

	if !match {
		return nil, errors.New("invalid credentials")
	}

	return s.token.IssueTokenPair(
		user.ID,
		user.Email,
		string(user.Role),
		user.TenantID,
		user.CustomerID,
	)
}

func (s *service) Me(ctx context.Context, identity string) (sqlc.GetUserDetailsRow, error) {
	user, err := s.repo.GetUserDetails(ctx, identity)
	if err != nil {
		return sqlc.GetUserDetailsRow{}, db.WrapDBError(ctx, err)
	}

	return user, nil
}

func (s *service) Refresh(ctx context.Context, identity string) (*jwt.Tokens, error) {
	user, err := s.repo.FindUser(ctx, identity)
	if err != nil {
		return nil, db.WrapDBError(ctx, err)
	}

	// Issue a new access token
	return s.token.IssueTokenPair(
		user.ID,
		user.Email,
		string(user.Role),
		user.TenantID,
		user.CustomerID,
	)
}
